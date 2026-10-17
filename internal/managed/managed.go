// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package managed finds the resources Terraform already manages, from
// state, so that an import can leave them out instead of importing them a
// second time. State can hold secrets: it is read only when asked, and
// only resource types and IDs are kept.
package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// Resources are managed resources: where the state of each is, by
// "type id" (and "type arn", where a resource has an ARN).
type Resources map[string]string

// Lookup returns where the state of a resource of typ with any of ids is.
func (r Resources) Lookup(typ string, ids ...string) (string, bool) {
	for _, id := range ids {
		if where, ok := r[typ+" "+id]; ok && id != "" {
			return where, true
		}
	}
	return "", false
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
// names the bucket's region, ?profile= the profile to read it with),
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

// isAzureBlob tells whether source is a Blob Storage URL, whose host is
// <account>.blob.<the cloud's storage suffix>.
func isAzureBlob(source string) bool {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" {
		return false
	}
	_, rest, _ := strings.Cut(u.Hostname(), ".")
	return strings.HasPrefix(rest, "blob.")
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
	found := 0
	for _, key := range keys {
		if !strings.HasSuffix(key, ".tfstate") {
			continue
		}
		found++
		content, err := store.Get(ctx, bucket, key)
		if err != nil {
			return err
		}
		if err := r.Parse(content, base+key); err != nil {
			return err
		}
	}
	if found == 0 {
		return errors.New("no .tfstate object under the prefix")
	}
	return nil
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
	for _, name := range names {
		content, err := store.Get(ctx, organization, name)
		if err != nil {
			return err
		}
		// A workspace with no state yet manages nothing.
		if content == nil {
			continue
		}
		if err := r.Parse(content, base+name); err != nil {
			return err
		}
	}
	return nil
}

func (r Resources) loadLocal(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		content, err := os.ReadFile(source)
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
		content, err := os.ReadFile(path)
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

// Parse adds the managed resources of a state file found at where, in
// format version 4, as Terraform 0.12 and later and OpenTofu write it.
// Version 3, from Terraform 0.11 and earlier, is refused with a hint to
// upgrade it.
func (r Resources) Parse(content []byte, where string) error {
	var state struct {
		Version   int `json:"version"`
		Resources []struct {
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Instances []struct {
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
					r[res.Type+" "+id] = where
				}
			}
		}
	}
	return nil
}
