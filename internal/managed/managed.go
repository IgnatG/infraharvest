// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package managed finds the resources Terraform already manages, from
// state, so that an import can leave them out instead of importing them a
// second time. State can hold secrets: it is read only when asked, and
// only resource types, IDs and addresses are kept.
package managed

import (
	"bytes"
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

	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// ObjectStore reads state from a bucket.
type ObjectStore interface {
	// List returns the keys under prefix.
	List(ctx context.Context, bucket, prefix string) ([]string, error)
	Get(ctx context.Context, bucket, key string) ([]byte, error)
}

// StreamStore is an ObjectStore that can also open an object to read as
// it goes: its states are decoded as they are read (see ParseFrom),
// rather than read whole first. Open returns nil, and no error, for an
// object that has no state, as Get does.
type StreamStore interface {
	ObjectStore
	Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// opener returns how to read the objects of bucket in store: as a stream
// where store is a StreamStore, else whole.
func opener(store ObjectStore, bucket string) func(ctx context.Context, key string) (io.ReadCloser, error) {
	if s, ok := store.(StreamStore); ok {
		return func(ctx context.Context, key string) (io.ReadCloser, error) { return s.Open(ctx, bucket, key) }
	}
	return func(ctx context.Context, key string) (io.ReadCloser, error) {
		content, err := store.Get(ctx, bucket, key)
		if err != nil || content == nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(content)), nil
	}
}

// Resources are managed resources, by "type id" (and "type arn", where a
// resource has an ARN, and "type identity", where its state has the
// resource identity, by its ID, see tftypes.IdentityID), as key makes
// them.
type Resources map[string]Resource

// Resource is a managed resource: Where its state is, and its Type and
// Address there, such as module.vpc.aws_subnet.a[0], and its ID there,
// its id attribute, or its ARN where it has no id.
type Resource struct {
	Where, Type, Address, ID string
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

// key is how Resources keys a resource of typ with id: by the type its
// other names share (see tftypes.Canonical), so that a load balancer
// managed as aws_alb is the aws_lb listed, and an Azure resource ID in
// lower case, since Azure doesn't keep one case for them:
// resourceGroups/App in Resource Graph can be resourcegroups/app in
// state. ARNs and Google self-links keep their case, which matters for
// them.
func key(typ, id string) string {
	typ = tftypes.Canonical(typ)
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

// DefaultHCPTerraformHost is the host of HCP Terraform (formerly Terraform
// Cloud); Terraform Enterprise has its own.
const DefaultHCPTerraformHost = "app.terraform.io"

// Stores open the object stores state is read from: S3 in a region, with
// the credentials of a shared config profile ("" for the defaults), Cloud
// Storage, the Azure Blob Storage account at a service URL, and the
// workspaces of HCP Terraform or Terraform Enterprise at a host. Each
// store is a package of its own under stores/, which links its cloud's
// SDK: the command that builds Stores includes only the clouds compiled
// in, so this package links none.
type Stores struct {
	S3           func(ctx context.Context, region, profile string) (ObjectStore, error)
	GCS          func(ctx context.Context) (ObjectStore, error)
	AzureBlob    func(ctx context.Context, serviceURL string) (ObjectStore, error)
	HCPTerraform func(ctx context.Context, host string) (ObjectStore, error)
}

// errNoStore is the error of a source whose kind of store, such as S3, the
// Stores given have no way to open: a caller that leaves one out reads
// no state of that kind, and says so rather than panicking.
func errNoStore(kind string) error {
	return fmt.Errorf("no %s store to read the state with", kind)
}

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

// The hosts of Blob Storage in Azure's clouds: public, US Government and
// China.
const (
	publicBlobSuffix     = "blob.core.windows.net"
	governmentBlobSuffix = "blob.core.usgovcloudapi.net"
	chinaBlobSuffix      = "blob.core.chinacloudapi.cn"
)

// azureBlobSuffixes are the hosts of Blob Storage in Azure's clouds. The
// credentials the azurerm backend uses go to the host a source names, so
// no other host is read from.
var azureBlobSuffixes = []string{publicBlobSuffix, governmentBlobSuffix, chinaBlobSuffix}

// AzureBlobSuffix returns the host of Blob Storage in the Azure cloud
// environment names, as ARM_ENVIRONMENT does to the azurerm backend when
// its configuration names none, as the configuration file's doesn't. It
// takes the names the Azure provider command takes, such as usgovernment
// or AzureChinaCloud, by dropping a leading "azure" and a trailing
// "cloud" first; "" is the public cloud.
func AzureBlobSuffix(environment string) (string, error) {
	switch strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(environment)), "azure"), "cloud") {
	case "", "public", "global":
		return publicBlobSuffix, nil
	case "usgovernment", "usgovernmentl4", "usgovernmentl5", "dod":
		return governmentBlobSuffix, nil
	case "china":
		return chinaBlobSuffix, nil
	}
	return "", fmt.Errorf("ARM_ENVIRONMENT %q: state is read from Blob Storage in Azure's public, US Government and China clouds only", environment)
}

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
		if stores.GCS == nil {
			return errNoStore("Cloud Storage")
		}
		store, err = stores.GCS(ctx)
	case "s3":
		if stores.S3 == nil {
			return errNoStore("S3")
		}
		store, err = stores.S3(ctx, o.url.Query().Get("region"), o.url.Query().Get("profile"))
	default:
		if stores.AzureBlob == nil {
			return errNoStore("Blob Storage")
		}
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
	return r.parseAll(ctx, states, opener(store, bucket), base)
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
	if stores.HCPTerraform == nil {
		return errNoStore("HCP Terraform")
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
	return r.parseAll(ctx, names, opener(store, organization), base)
}

// fetchParallel is how many states parseAll reads at once.
const fetchParallel = 8

// parseAll reads the state at each of keys with open, fetchParallel at a
// time, decoding each as it is read, and adds what each manages to r,
// found at base+key. A key open returns no state for, such as a workspace
// with none yet, manages nothing. The first failure stops the rest and is returned. What the
// states manage is added in the order of keys, as if read one by one.
func (r Resources) parseAll(ctx context.Context, keys []string, open func(ctx context.Context, key string) (io.ReadCloser, error), base string) error {
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
			in, err := open(ctx, key)
			if err != nil {
				fail(err)
				return
			}
			if in == nil {
				return
			}
			defer func() { _ = in.Close() }()
			state := Resources{}
			// A store whose URLs carry a credential returns its errors
			// without them (see WithoutURL), those of reading too.
			if err := state.ParseFrom(in, base+key); err != nil {
				fail(WithoutURL(err))
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
		return r.parseStateFile(source)
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
		return r.parseStateFile(path)
	})
	if err == nil && found == 0 {
		return errors.New("no .tfstate file in the directory, outside .terraform")
	}
	return err
}

// stateSizeLimit is the most bytes one state may take: a store may read
// state whole into memory (see ReadState), and an object that big under a
// prefix is most likely not state.
var stateSizeLimit int64 = 512 << 20

// ReadState reads one state from r, failing rather than cutting it off if
// it takes more than stateSizeLimit. Every ObjectStore reads what it gets
// through it, so that each source has the same limit.
func ReadState(r io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(r, stateSizeLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > stateSizeLimit {
		return nil, fmt.Errorf("state is larger than %d MiB, the most infraharvest reads", stateSizeLimit>>20)
	}
	return content, nil
}

// WithoutURL returns the error a *url.Error wraps, whose message quotes
// the URL, or err. A store whose URLs carry a credential, such as a SAS
// token or a signed download URL, returns its errors through it, so that
// no error, log or MCP client sees one.
func WithoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// parseStateFile adds what the state file at path manages, read as it is
// decoded (see ParseFrom).
func (r Resources) parseStateFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return r.ParseFrom(f, path)
}

// Parse adds the managed resources of a state file found at where, in
// format version 4, as Terraform 0.12 and later and OpenTofu write it.
// Version 3, from Terraform 0.11 and earlier, is refused with a hint to
// upgrade it. Each resource has its address in the state, or none where
// the state doesn't name it.
func (r Resources) Parse(content []byte, where string) error {
	return r.ParseFrom(bytes.NewReader(content), where)
}

// ParseFrom is Parse of the state in, read as it is decoded: one resource
// of the state is held at a time, and of it only what Parse keeps, so that
// a large state doesn't take its size in memory twice over. It fails, as
// ReadState does, on a state larger than stateSizeLimit, and, as Parse
// does, adds nothing from a state it fails on.
func (r Resources) ParseFrom(in io.Reader, where string) error {
	found := Resources{}
	version, err := decodeState(json.NewDecoder(&limitedState{r: in}), found, where)
	if err != nil {
		var tooLarge *stateTooLarge
		if errors.As(err, &tooLarge) {
			return tooLarge
		}
		return fmt.Errorf("%s: %w", where, err)
	}
	switch version {
	case 4:
	case 3:
		return errors.New(where + ": state format version 3 is not supported; upgrade it with Terraform 0.12 or later")
	default:
		return errors.New(where + ": not a version 4 state file")
	}
	maps.Copy(r, found)
	return nil
}

// stateResource is what Parse reads of a resource of a state.
type stateResource struct {
	Mode      string `json:"mode"`
	Module    string `json:"module"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Instances []struct {
		IndexKey   any            `json:"index_key"`
		Identity   map[string]any `json:"identity"`
		Attributes struct {
			ID  string `json:"id"`
			ARN string `json:"arn"`
		} `json:"attributes"`
	} `json:"instances"`
}

// decodeState reads a state from dec as encoding/json would unmarshal it
// into a struct of its version and its resources (see stateResource),
// one resource at a time, adding each managed one to found as it goes,
// and returns its version. Keys match as encoding/json matches them,
// whatever their case, and of a key given twice, the last counts; for
// resources, encoding/json would decode the second array into the
// elements of the first, which no state has (see FuzzParseFromIsParseWhole).
func decodeState(dec *json.Decoder, found Resources, where string) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	version := 0
	switch tok {
	case nil:
		// null, which unmarshals into the zero state.
	case json.Delim('{'):
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return 0, err
			}
			name, _ := tok.(string)
			switch {
			case strings.EqualFold(name, "version"):
				if err := dec.Decode(&version); err != nil {
					return 0, err
				}
			case strings.EqualFold(name, "resources"):
				clear(found)
				if err := decodeResources(dec, found, where); err != nil {
					return 0, err
				}
			default:
				if err := skipValue(dec); err != nil {
					return 0, err
				}
			}
		}
		if _, err := dec.Token(); err != nil { // the closing brace
			return 0, err
		}
	default:
		return 0, fmt.Errorf("a state is a JSON object, not %v", tok)
	}
	// Nothing may follow the state, as encoding/json.Unmarshal says.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("invalid character after top-level value")
		}
		return 0, err
	}
	return version, nil
}

// decodeResources reads the resources array of a state from dec, one
// resource at a time, adding the managed ones to found.
func decodeResources(dec *json.Decoder, found Resources, where string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case nil:
		return nil
	case json.Delim('['):
	default:
		return fmt.Errorf("a state's resources are an array, not %v", tok)
	}
	for dec.More() {
		var res stateResource
		if err := dec.Decode(&res); err != nil {
			return err
		}
		addResource(found, res, where)
	}
	_, err = dec.Token() // the closing bracket
	return err
}

// addResource adds res, one resource of a state found at where, to found,
// if it is managed.
func addResource(found Resources, res stateResource, where string) {
	if res.Mode != "managed" {
		return
	}
	for _, i := range res.Instances {
		stateID := i.Attributes.ID
		if stateID == "" {
			stateID = i.Attributes.ARN
		}
		ids := []string{i.Attributes.ID, i.Attributes.ARN}
		// What Terraform's list resources return is listed by its
		// identity's ID, which the state of a resource has from
		// Terraform 1.12 on, whatever it was imported by.
		if identity, ok := tftypes.IdentityValues(i.Identity); ok {
			ids = append(ids, tftypes.IdentityID(identity))
		}
		for _, id := range ids {
			if id != "" {
				found[key(res.Type, id)] = Resource{Where: where, Type: res.Type, Address: address(res.Module, res.Type, res.Name, i.IndexKey), ID: stateID}
			}
		}
	}
}

// skipValue reads the next value from dec, whatever it is, without
// keeping it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// limitedState reads r, and fails once it has read more than
// stateSizeLimit bytes (see ReadState).
type limitedState struct {
	r    io.Reader
	read int64
}

// stateTooLarge is the error of a state larger than stateSizeLimit.
type stateTooLarge struct{}

func (*stateTooLarge) Error() string {
	return fmt.Sprintf("state is larger than %d MiB, the most infraharvest reads", stateSizeLimit>>20)
}

func (l *limitedState) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > stateSizeLimit {
		return n, &stateTooLarge{}
	}
	return n, err
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
