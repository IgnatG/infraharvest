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

// Stores open the object stores state is read from: S3 in a region ("" for
// the default), and Cloud Storage.
type Stores struct {
	S3  func(ctx context.Context, region string) (ObjectStore, error)
	GCS func(ctx context.Context) (ObjectStore, error)
}

// DefaultStores read with each cloud's default credentials.
var DefaultStores = Stores{S3: NewS3, GCS: NewGCS}

// Load reads the state in sources: state files, directories with state
// files (*.tfstate, outside .terraform), s3://bucket/prefix (?region=
// names the bucket's region) and gs://bucket/prefix, all of whose
// *.tfstate objects are read, from stores.
func Load(ctx context.Context, sources []string, stores Stores) (Resources, error) {
	r := Resources{}
	for _, source := range sources {
		var err error
		switch {
		case strings.HasPrefix(source, "s3://"), strings.HasPrefix(source, "gs://"):
			err = r.loadObjects(ctx, source, stores)
		default:
			err = r.loadLocal(source)
		}
		if err != nil {
			return nil, fmt.Errorf("state %s: %w", source, err)
		}
	}
	return r, nil
}

// loadObjects reads the *.tfstate objects under an s3:// or gs:// source.
func (r Resources) loadObjects(ctx context.Context, source string, stores Stores) error {
	u, err := url.Parse(source)
	if err != nil {
		return err
	}
	var store ObjectStore
	if u.Scheme == "gs" {
		store, err = stores.GCS(ctx)
	} else {
		store, err = stores.S3(ctx, u.Query().Get("region"))
	}
	if err != nil {
		return err
	}
	prefix := strings.TrimPrefix(u.Path, "/")
	keys, err := store.List(ctx, u.Host, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !strings.HasSuffix(key, ".tfstate") {
			continue
		}
		content, err := store.Get(ctx, u.Host, key)
		if err != nil {
			return err
		}
		if err := r.Parse(content, u.Scheme+"://"+u.Host+"/"+key); err != nil {
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
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".tfstate") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return r.Parse(content, path)
	})
}

// Parse adds the managed resources of a state file found at where: format
// version 4, as Terraform and OpenTofu write it, or 3 (see parseV3).
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
	case 3:
		return r.parseV3(content, where)
	case 4:
	default:
		return errors.New(where + ": not a version 3 or 4 state file")
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

// parseV3 adds the managed resources of a version 3 state file, as
// Terraform 0.11 and Terraformer (the legacy engine) write it.
func (r Resources) parseV3(content []byte, where string) error {
	var state struct {
		Modules []struct {
			Resources map[string]struct {
				Type    string `json:"type"`
				Primary struct {
					ID         string            `json:"id"`
					Attributes map[string]string `json:"attributes"`
				} `json:"primary"`
			} `json:"resources"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	for _, m := range state.Modules {
		for address, res := range m.Resources {
			if strings.HasPrefix(address, "data.") {
				continue
			}
			for _, id := range []string{res.Primary.ID, res.Primary.Attributes["arn"]} {
				if id != "" {
					r[res.Type+" "+id] = where
				}
			}
		}
	}
	return nil
}
