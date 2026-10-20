// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package managed finds the resources Terraform already manages, from
// state, so that an import can leave them out instead of importing them a
// second time. State can hold secrets: it is read only when asked, and
// only resource types, IDs and addresses are kept.
package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Reason starts the reason an import gives for leaving out a resource
// Terraform already manages.
const Reason = "already managed by Terraform"

// ObjectStore reads state from a bucket.
type ObjectStore interface {
	// List returns the keys under prefix.
	List(ctx context.Context, bucket, prefix string) ([]string, error)
	Get(ctx context.Context, bucket, key string) ([]byte, error)
}

// Resources are managed resources, by "type id" (and "type arn", where a
// resource has an ARN), as key makes them.
type Resources map[string]Resource

// Resource is a managed resource: Where its state is, and its Type and
// Address there, such as module.vpc.aws_subnet.a[0].
type Resource struct {
	Where, Type, Address string
}

// Lookup returns where the state of a resource of typ with any of ids is.
func (r Resources) Lookup(typ string, ids ...string) (string, bool) {
	res, ok := r.Find(typ, ids...)
	return res.Where, ok
}

// Find returns the managed resource of typ with any of ids.
func (r Resources) Find(typ string, ids ...string) (Resource, bool) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if res, ok := r[key(typ, id)]; ok {
			return res, true
		}
	}
	return Resource{}, false
}

// typeAliases maps resource types to the type another name of theirs is
// matched as: a load balancer managed as aws_alb is the aws_lb listed, and
// an Azure virtual machine managed as azurerm_virtual_machine is the
// azurerm_linux_virtual_machine or azurerm_windows_virtual_machine listed.
// The types of each group have the same IDs.
var typeAliases = map[string]string{
	"aws_alb":                                        "aws_lb",
	"aws_alb_listener":                               "aws_lb_listener",
	"aws_alb_listener_certificate":                   "aws_lb_listener_certificate",
	"aws_alb_listener_rule":                          "aws_lb_listener_rule",
	"aws_alb_target_group":                           "aws_lb_target_group",
	"aws_alb_target_group_attachment":                "aws_lb_target_group_attachment",
	"azurerm_linux_virtual_machine":                  "azurerm_virtual_machine",
	"azurerm_windows_virtual_machine":                "azurerm_virtual_machine",
	"azurerm_linux_virtual_machine_scale_set":        "azurerm_virtual_machine_scale_set",
	"azurerm_windows_virtual_machine_scale_set":      "azurerm_virtual_machine_scale_set",
	"azurerm_orchestrated_virtual_machine_scale_set": "azurerm_virtual_machine_scale_set",
	"google_compute_instance_from_template":          "google_compute_instance",
}

// key is how Resources keys a resource of typ with id: by the type its
// aliases share (see typeAliases), and an Azure resource ID in lower case,
// since Azure doesn't keep one case for them: resourceGroups/App in
// Resource Graph can be resourcegroups/app in state. ARNs and Google
// self-links keep their case, which matters for them.
func key(typ, id string) string {
	if alias, ok := typeAliases[typ]; ok {
		typ = alias
	}
	if isAzureID(id) {
		id = strings.ToLower(id)
	}
	return typ + " " + id
}

// isAzureID reports whether id is an Azure Resource Manager ID, which
// starts with /subscriptions/ or /providers/ in any case.
func isAzureID(id string) bool {
	for _, prefix := range []string{"/subscriptions/", "/providers/"} {
		if len(id) >= len(prefix) && strings.EqualFold(id[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// Stores open the object stores state is read from: S3 in a region, with
// the credentials of a shared config profile ("" for the defaults), Cloud
// Storage, the Azure Blob Storage account at a service URL, and the
// workspaces of HCP Terraform or Terraform Enterprise at a host (see
// HCPTerraform).
type Stores struct {
	S3           func(ctx context.Context, region, profile string) (ObjectStore, error)
	GCS          func(ctx context.Context) (ObjectStore, error)
	AzureBlob    func(ctx context.Context, serviceURL string) (ObjectStore, error)
	HCPTerraform func(ctx context.Context, host string) (ObjectStore, error)
}

// DefaultStores read with each cloud's default credentials.
var DefaultStores = Stores{S3: NewS3, GCS: NewGCS, AzureBlob: NewAzureBlob, HCPTerraform: NewHCPTerraform}

// Load reads the state in sources: state files, directories with state
// files (*.tfstate, outside .terraform), s3://bucket/prefix (?region=
// names the bucket's region, ?profile= the profile to read it with,
// ?workspace_key_prefix= where its workspaces' states are, see
// s3WorkspaceStates),
// gs://bucket/prefix and https://<account>.blob.core.windows.net/
// container/prefix, all of whose *.tfstate objects are read, and
// tfc://organization/workspace, the current state of HCP Terraform
// workspaces (see loadWorkspaces), from stores. A source that holds no
// state, such as a directory without state files or a workspace pattern
// that matches no workspace, fails: it is most likely mistyped, and
// leaving out nothing would import what it manages a second time.
func Load(ctx context.Context, sources []string, stores Stores) (Resources, error) {
	r := Resources{}
	for _, source := range sources {
		var err error
		switch {
		case strings.HasPrefix(source, "s3://"), strings.HasPrefix(source, "gs://"), isAzureBlob(source):
			err = r.loadObjects(ctx, source, stores)
		case strings.HasPrefix(source, "tfc://"):
			err = r.loadWorkspaces(ctx, source, stores)
		case isURL(source):
			err = errNotBlobStorage
		default:
			err = r.loadLocal(source)
		}
		if err != nil {
			return nil, fmt.Errorf("state %s: %w", source, err)
		}
	}
	return r, nil
}

// CheckSources rejects the sources Load couldn't read from what they say
// alone, without reading any state: a URL that doesn't parse or names no
// bucket, container, organization or workspace, and a local path that
// doesn't exist.
func CheckSources(sources []string) error {
	for _, source := range sources {
		var err error
		switch {
		case strings.HasPrefix(source, "s3://"), strings.HasPrefix(source, "gs://"), isAzureBlob(source):
			_, err = parseObjects(source)
		case strings.HasPrefix(source, "tfc://"):
			_, _, _, err = parseWorkspaces(source)
		case isURL(source):
			err = errNotBlobStorage
		default:
			_, err = os.Stat(source)
		}
		if err != nil {
			return fmt.Errorf("state %s: %w", source, err)
		}
	}
	return nil
}

// objectSource is an s3://, gs:// or Blob Storage source: the bucket, or
// container, its objects are in, the prefix of their keys, and base,
// where their names start from.
type objectSource struct {
	url                  *url.URL
	bucket, prefix, base string
}

// parseObjects parses an s3://, gs:// or Blob Storage source.
func parseObjects(source string) (objectSource, error) {
	u, err := url.Parse(source)
	if err != nil {
		return objectSource{}, err
	}
	o := objectSource{url: u, bucket: u.Host, prefix: strings.TrimPrefix(u.Path, "/"), base: u.Scheme + "://" + u.Host + "/"}
	if u.Scheme == "s3" || u.Scheme == "gs" {
		if o.bucket == "" {
			return objectSource{}, fmt.Errorf("name a bucket: %s://<bucket>/<prefix>", u.Scheme)
		}
		return o, nil
	}
	// https://<account>.blob.core.windows.net/<container>/<prefix>
	o.bucket, o.prefix, _ = strings.Cut(o.prefix, "/")
	if o.bucket == "" {
		return objectSource{}, errors.New("name a container: https://<account>.blob.core.windows.net/<container>/<prefix>")
	}
	o.base += o.bucket + "/"
	return o, nil
}

// parseWorkspaces parses a tfc://organization/workspace source into the
// organization, the workspace or pattern, and the host (?host=, or
// DefaultHCPTerraformHost).
func parseWorkspaces(source string) (organization, pattern, host string, err error) {
	u, err := url.Parse(source)
	if err != nil {
		return "", "", "", err
	}
	organization, pattern = u.Host, strings.TrimPrefix(u.Path, "/")
	if organization == "" || pattern == "" {
		return "", "", "", errors.New("name an organization and a workspace: tfc://<organization>/<workspace>, or <prefix>* for several")
	}
	host = u.Query().Get("host")
	if host == "" {
		host = DefaultHCPTerraformHost
	}
	return organization, pattern, host, nil
}

// azureBlobSuffixes are the hosts of Blob Storage in Azure's clouds:
// public, US Government and China. The credentials the azurerm backend
// uses go to the host a source names, so no other host is read from.
var azureBlobSuffixes = []string{"blob.core.windows.net", "blob.core.usgovcloudapi.net", "blob.core.chinacloudapi.cn"}

// isAzureBlob tells whether source is a Blob Storage URL, whose host is
// <account>.<one of azureBlobSuffixes>.
func isAzureBlob(source string) bool {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Port() != "" {
		return false
	}
	account, rest, _ := strings.Cut(strings.ToLower(u.Hostname()), ".")
	return account != "" && slices.Contains(azureBlobSuffixes, rest)
}

// errNotBlobStorage rejects an http or https source that isn't a Blob
// Storage URL, which would get the Azure credentials otherwise.
var errNotBlobStorage = errors.New("not a Blob Storage URL: the host must be <account>." + strings.Join(azureBlobSuffixes, ", <account>."))

// isURL tells whether source is an http or https URL.
func isURL(source string) bool {
	return strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")
}

// loadObjects reads the *.tfstate objects under an s3://, gs:// or Blob
// Storage source.
func (r Resources) loadObjects(ctx context.Context, source string, stores Stores) error {
	o, err := parseObjects(source)
	if err != nil {
		return err
	}
	var store ObjectStore
	switch o.url.Scheme {
	case "gs":
		store, err = stores.GCS(ctx)
	case "s3":
		store, err = stores.S3(ctx, o.url.Query().Get("region"), o.url.Query().Get("profile"))
	default:
		store, err = stores.AzureBlob(ctx, o.url.Scheme+"://"+o.url.Host+"/")
	}
	if err != nil {
		return err
	}
	bucket, base := o.bucket, o.base
	keys, err := store.List(ctx, bucket, o.prefix)
	if err != nil {
		return err
	}
	var states []string
	for _, key := range keys {
		if isStateObject(o.url.Scheme, key) {
			states = append(states, key)
		}
	}
	if o.url.Scheme == "s3" {
		workspaces, err := s3WorkspaceStates(ctx, store, o)
		if err != nil {
			return err
		}
		states = append(states, workspaces...)
	}
	if len(states) == 0 {
		return errors.New("no .tfstate object under the prefix")
	}
	return r.parseAll(ctx, states, func(ctx context.Context, key string) ([]byte, error) {
		return store.Get(ctx, bucket, key)
	}, base)
}

// defaultWorkspaceKeyPrefix is the S3 backend's workspace_key_prefix when
// the configuration sets none, as the roots infraharvest writes don't.
const defaultWorkspaceKeyPrefix = "env:"

// s3WorkspaceStates returns the keys of the states the S3 backend keeps
// for the workspaces other than default of the keys under o's prefix. It
// keeps them at <workspace_key_prefix>/<workspace>/<key>, at the bucket's
// root rather than under the prefix, so they are listed on their own:
// with the source's ?workspace_key_prefix=, or else "env:". A source whose
// prefix is empty, or is itself under the workspaces' prefix, already
// lists them, and nothing more is asked for.
func s3WorkspaceStates(ctx context.Context, store ObjectStore, o objectSource) ([]string, error) {
	workspaceKeyPrefix := o.url.Query().Get("workspace_key_prefix")
	if workspaceKeyPrefix == "" {
		workspaceKeyPrefix = defaultWorkspaceKeyPrefix
	}
	workspaceKeyPrefix = strings.TrimSuffix(workspaceKeyPrefix, "/") + "/"
	if o.prefix == "" || strings.HasPrefix(o.prefix, workspaceKeyPrefix) {
		return nil, nil
	}
	keys, err := store.List(ctx, o.bucket, workspaceKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("workspace states under %s: %w", workspaceKeyPrefix, err)
	}
	var states []string
	for _, key := range keys {
		_, workspaceKey, ok := strings.Cut(strings.TrimPrefix(key, workspaceKeyPrefix), "/")
		if ok && strings.HasPrefix(workspaceKey, o.prefix) && isStateObject("s3", key) {
			states = append(states, key)
		}
	}
	return states, nil
}

// isStateObject reports whether the object at key, in a store of scheme
// (s3, gs or https for Blob Storage), is state: a *.tfstate object, or in
// Blob Storage, where the azurerm backend keeps the state of a workspace
// other than default at <key>env:<workspace>, one of those.
func isStateObject(scheme, key string) bool {
	return strings.HasSuffix(key, ".tfstate") || (scheme == "https" && strings.Contains(key, ".tfstateenv:"))
}

// loadWorkspaces reads the current state of the workspaces a
// tfc://organization/workspace source names: that workspace, or, ending in
// *, every workspace whose name starts with what comes before. ?host=
// names a Terraform Enterprise host.
func (r Resources) loadWorkspaces(ctx context.Context, source string, stores Stores) error {
	organization, pattern, host, err := parseWorkspaces(source)
	if err != nil {
		return err
	}
	store, err := stores.HCPTerraform(ctx, host)
	if err != nil {
		return err
	}
	names := []string{pattern}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		if names, err = store.List(ctx, organization, prefix); err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("no workspace of organization %s has a name starting with %q", organization, prefix)
		}
	}
	base := "tfc://" + organization + "/"
	if host != DefaultHCPTerraformHost {
		base = "tfc://" + host + "/" + organization + "/"
	}
	return r.parseAll(ctx, names, func(ctx context.Context, name string) ([]byte, error) {
		return store.Get(ctx, organization, name)
	}, base)
}

// fetchParallel is how many states parseAll reads at once.
const fetchParallel = 8

// parseAll reads the state at each of keys with get, fetchParallel at a
// time, and adds what each manages to r, found at base+key. A key get
// returns no state for, such as a workspace with none yet, manages
// nothing. The first failure stops the rest and is returned. What the
// states manage is added in the order of keys, as if read one by one.
func (r Resources) parseAll(ctx context.Context, keys []string, get func(ctx context.Context, key string) ([]byte, error), base string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	parsed := make([]Resources, len(keys))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
		cancel()
	}
	slots := make(chan struct{}, fetchParallel)
	for i, key := range keys {
		slots <- struct{}{}
		if ctx.Err() != nil {
			<-slots
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			content, err := get(ctx, key)
			if err != nil {
				fail(err)
				return
			}
			if content == nil {
				return
			}
			state := Resources{}
			if err := state.Parse(content, base+key); err != nil {
				fail(err)
				return
			}
			parsed[i] = state
		})
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, state := range parsed {
		maps.Copy(r, state)
	}
	return nil
}

func (r Resources) loadLocal(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		content, err := readStateFile(source)
		if err != nil {
			return err
		}
		return r.Parse(content, source)
	}
	found := 0
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".tfstate") {
			return nil
		}
		found++
		content, err := readStateFile(path)
		if err != nil {
			return err
		}
		return r.Parse(content, path)
	})
	if err == nil && found == 0 {
		return errors.New("no .tfstate file in the directory, outside .terraform")
	}
	return err
}

// stateSizeLimit is the most bytes one state may take: state is read whole
// into memory, and an object that big under a prefix is most likely not
// state.
var stateSizeLimit int64 = 512 << 20

// readState reads one state from r, failing rather than cutting it off if
// it takes more than stateSizeLimit.
func readState(r io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(r, stateSizeLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > stateSizeLimit {
		return nil, fmt.Errorf("state is larger than %d MiB, the most infraharvest reads", stateSizeLimit>>20)
	}
	return content, nil
}

// readStateFile reads the state file at path (see readState).
func readStateFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readState(f)
}

// Parse adds the managed resources of a state file found at where, in
// format version 4, as Terraform 0.12 and later and OpenTofu write it.
// Version 3, from Terraform 0.11 and earlier, is refused with a hint to
// upgrade it. Each resource has its address in the state, or none where
// the state doesn't name it.
func (r Resources) Parse(content []byte, where string) error {
	var state struct {
		Version   int `json:"version"`
		Resources []struct {
			Mode      string `json:"mode"`
			Module    string `json:"module"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				IndexKey   any `json:"index_key"`
				Attributes struct {
					ID  string `json:"id"`
					ARN string `json:"arn"`
				} `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	switch state.Version {
	case 4:
	case 3:
		return errors.New(where + ": state format version 3 is not supported; upgrade it with Terraform 0.12 or later")
	default:
		return errors.New(where + ": not a version 4 state file")
	}
	for _, res := range state.Resources {
		if res.Mode != "managed" {
			continue
		}
		for _, i := range res.Instances {
			for _, id := range []string{i.Attributes.ID, i.Attributes.ARN} {
				if id != "" {
					r[key(res.Type, id)] = Resource{Where: where, Type: res.Type, Address: address(res.Module, res.Type, res.Name, i.IndexKey)}
				}
			}
		}
	}
	return nil
}

// address returns the address state gives the instance with index, its
// instance key (a number for count, a string for for_each, or none), of
// the resource of typ named name in module, such as
// module.vpc.aws_subnet.a[0]; "" if the state names no resource.
func address(module, typ, name string, index any) string {
	if name == "" {
		return ""
	}
	a := typ + "." + name
	if module != "" {
		a = module + "." + a
	}
	switch k := index.(type) {
	case float64:
		a += "[" + strconv.FormatFloat(k, 'f', -1, 64) + "]"
	case string:
		a += "[" + strconv.Quote(k) + "]"
	}
	return a
}
