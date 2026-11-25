// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

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

// ReadAll reads the state each of locations names (see Read), in order,
// and returns what they hold in one list, the first failure ending it.
func ReadAll(ctx context.Context, locations []string, stores Stores) ([]Document, error) {
	var docs []Document
	for _, location := range locations {
		found, err := Read(ctx, location, stores)
		if err != nil {
			return nil, err
		}
		docs = append(docs, found...)
	}
	return docs, nil
}

// Read reads the state a location names, a Document for each state:
//
//   - a state file, or a directory, whose *.tfstate files are read, outside
//     any .terraform directory;
//   - s3://bucket/prefix, where ?region= names the bucket's region,
//     ?profile= the profile to read it with and ?workspace_key_prefix=
//     where its workspaces' states are (env: by default), and
//     gs://bucket/prefix, or
//     https://<account>.blob.core.windows.net/container/prefix (or the
//     US Government or China host), all of whose *.tfstate objects are
//     read, and the workspaces of the s3 backend and of the azurerm
//     backend with them;
//   - tfc://organization/workspace, the current state of an HCP Terraform
//     workspace, or tfc://organization/prefix* of every workspace whose
//     name starts so; ?host= names a Terraform Enterprise host.
//
// A Document's Source says where its state was found: the path of a file,
// or the location of an object, such as s3://bucket/key.
//
// A location that holds no state, such as a directory without state files
// or a workspace pattern that matches no workspace, fails: it is most
// likely mistyped, and reading nothing would pass for a root with no
// resources. So does a kind of location whose store the Stores given leave
// out. Several states are fetched at a time, and the Documents come in the
// order of the states' keys. The first failure stops the rest.
func Read(ctx context.Context, location string, stores Stores) ([]Document, error) {
	var (
		docs []Document
		err  error
	)
	switch {
	case strings.HasPrefix(location, "s3://"), strings.HasPrefix(location, "gs://"), isAzureBlob(location):
		docs, err = readObjects(ctx, location, stores)
	case strings.HasPrefix(location, "tfc://"):
		docs, err = readWorkspaces(ctx, location, stores)
	case isURL(location):
		err = errNotBlobStorage
	default:
		docs, err = readLocal(location)
	}
	if err != nil {
		return nil, fmt.Errorf("state %s: %w", location, err)
	}
	return docs, nil
}

// CheckLocations rejects the locations Read couldn't read from what they
// say alone, without reading any state: a URL that doesn't parse or names
// no bucket, container, organization or workspace, and a local path that
// doesn't exist.
func CheckLocations(locations []string) error {
	for _, location := range locations {
		var err error
		switch {
		case strings.HasPrefix(location, "s3://"), strings.HasPrefix(location, "gs://"), isAzureBlob(location):
			_, err = parseObjects(location)
		case strings.HasPrefix(location, "tfc://"):
			_, _, _, err = parseWorkspaces(location)
		case isURL(location):
			err = errNotBlobStorage
		default:
			_, err = os.Stat(location)
		}
		if err != nil {
			return fmt.Errorf("state %s: %w", location, err)
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

// readObjects reads the *.tfstate objects under an s3://, gs:// or Blob
// Storage source.
func readObjects(ctx context.Context, source string, stores Stores) ([]Document, error) {
	o, err := parseObjects(source)
	if err != nil {
		return nil, err
	}
	var store ObjectStore
	switch o.url.Scheme {
	case "gs":
		if stores.GCS == nil {
			return nil, errNoStore("Cloud Storage")
		}
		store, err = stores.GCS(ctx)
	case "s3":
		if stores.S3 == nil {
			return nil, errNoStore("S3")
		}
		store, err = stores.S3(ctx, o.url.Query().Get("region"), o.url.Query().Get("profile"))
	default:
		if stores.AzureBlob == nil {
			return nil, errNoStore("Blob Storage")
		}
		store, err = stores.AzureBlob(ctx, o.url.Scheme+"://"+o.url.Host+"/")
	}
	if err != nil {
		return nil, err
	}
	bucket, base := o.bucket, o.base
	keys, err := store.List(ctx, bucket, o.prefix)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		states = append(states, workspaces...)
	}
	if len(states) == 0 {
		return nil, errors.New("no .tfstate object under the prefix")
	}
	return parseAll(ctx, states, opener(store, bucket), base)
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

// readWorkspaces reads the current state of the workspaces a
// tfc://organization/workspace source names: that workspace, or, ending in
// *, every workspace whose name starts with what comes before. ?host=
// names a Terraform Enterprise host.
func readWorkspaces(ctx context.Context, source string, stores Stores) ([]Document, error) {
	organization, pattern, host, err := parseWorkspaces(source)
	if err != nil {
		return nil, err
	}
	if stores.HCPTerraform == nil {
		return nil, errNoStore("HCP Terraform")
	}
	store, err := stores.HCPTerraform(ctx, host)
	if err != nil {
		return nil, err
	}
	names := []string{pattern}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		if names, err = store.List(ctx, organization, prefix); err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no workspace of organization %s has a name starting with %q", organization, prefix)
		}
	}
	base := "tfc://" + organization + "/"
	if host != DefaultHCPTerraformHost {
		base = "tfc://" + host + "/" + organization + "/"
	}
	return parseAll(ctx, names, opener(store, organization), base)
}

// fetchParallel is how many states parseAll reads at once.
const fetchParallel = 8

// parseAll reads the state at each of keys with open, fetchParallel at a
// time, decoding each as it is read, and returns what each holds, found at
// base+key, in the order of keys. A key open returns no state for, such as
// a workspace with none yet, holds nothing. The first failure stops the
// rest and is returned.
func parseAll(ctx context.Context, keys []string, open func(ctx context.Context, key string) (io.ReadCloser, error), base string) ([]Document, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	parsed := make([]*Document, len(keys))
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
			// A store whose URLs carry a credential returns its errors
			// without them (see WithoutURL), those of reading too.
			doc, err := ParseFrom(in, base+key)
			if err != nil {
				fail(WithoutURL(err))
				return
			}
			parsed[i] = &doc
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var docs []Document
	for _, doc := range parsed {
		if doc != nil {
			docs = append(docs, *doc)
		}
	}
	return docs, nil
}

// readLocal reads the state file at path, or the *.tfstate files of the
// directory at path, outside .terraform.
func readLocal(path string) ([]Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		doc, err := parseStateFile(path)
		if err != nil {
			return nil, err
		}
		return []Document{doc}, nil
	}
	var docs []Document
	err = filepath.WalkDir(path, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".tfstate") {
			return nil
		}
		doc, err := parseStateFile(file)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
		return nil
	})
	if err == nil && len(docs) == 0 {
		return nil, errors.New("no .tfstate file in the directory, outside .terraform")
	}
	return docs, err
}

// parseStateFile reads the state file at path, as it is decoded (see
// ParseFrom).
func parseStateFile(path string) (Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return Document{}, err
	}
	defer f.Close()
	return ParseFrom(f, path)
}
