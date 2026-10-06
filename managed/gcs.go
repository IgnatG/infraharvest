// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"errors"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// GCS reads state from Cloud Storage with Application Default Credentials,
// as Terraform's gcs backend does.
type GCS struct {
	client *storage.Client
}

// NewGCS opens Cloud Storage.
func NewGCS(ctx context.Context) (ObjectStore, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &GCS{client: client}, nil
}

// List returns the names of the objects under prefix.
func (g *GCS) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	var names []string
	it := g.client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		o, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, o.Name)
	}
}

// Get returns an object's content.
func (g *GCS) Get(ctx context.Context, bucket, name string) ([]byte, error) {
	r, err := g.client.Bucket(bucket).Object(name).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
