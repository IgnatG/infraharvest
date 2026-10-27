// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package gcs reads Terraform state from Cloud Storage, where the gcs
// backend keeps it. It links Google's API client, so only a build with
// Google Cloud includes it.
package gcs

import (
	"context"
	"net/url"
	"os"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/IgnatG/infraharvest/internal/managed"
)

// Store reads state from Cloud Storage with Application Default
// Credentials, as Terraform's gcs backend does, through the JSON API.
type Store struct {
	service *storage.Service
}

// New opens Cloud Storage. When STORAGE_EMULATOR_HOST is set it reads
// from that emulator, without credentials, as Cloud Storage's own client
// libraries do; a host without a scheme is taken as http.
func New(ctx context.Context) (managed.ObjectStore, error) {
	opts := []option.ClientOption{option.WithScopes(storage.DevstorageReadOnlyScope)}
	if host := os.Getenv("STORAGE_EMULATOR_HOST"); host != "" {
		endpoint, err := emulatorEndpoint(host)
		if err != nil {
			return nil, err
		}
		opts = []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
	}
	service, err := storage.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Store{service: service}, nil
}

// emulatorEndpoint returns the JSON API's base URL on the emulator at host,
// a URL or a host[:port], which is taken as http.
func emulatorEndpoint(host string) (string, error) {
	hostURL := &url.URL{Scheme: "http", Host: host}
	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil {
			return "", err
		}
		hostURL = parsed
	}
	hostURL.Path = "/storage/v1/"
	return hostURL.String(), nil
}

// List returns the names of the objects under prefix, every page of them.
func (s *Store) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	var names []string
	err := s.service.Objects.List(bucket).Prefix(prefix).Fields("items/name", "nextPageToken").
		Pages(ctx, func(page *storage.Objects) error {
			for _, o := range page.Items {
				names = append(names, o.Name)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return names, nil
}

// Get returns an object's content.
func (s *Store) Get(ctx context.Context, bucket, name string) ([]byte, error) {
	resp, err := s.service.Objects.Get(bucket, name).Context(ctx).Download()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return managed.ReadState(resp.Body)
}
