// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3 serves one bucket's objects, path-style: ListObjectsV2, a key a
// page, and GetObject.
func fakeS3(t *testing.T, bucket string, objects map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/"+bucket)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/"+bucket) {
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("list-type") == "2" && (path == "" || path == "/") {
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range objects {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			start := 0
			if token := r.URL.Query().Get("continuation-token"); token != "" {
				start, _ = strconv.Atoi(token)
			}
			var page strings.Builder
			if start < len(keys) {
				fmt.Fprintf(&page, "<Contents><Key>%s</Key></Contents>", keys[start])
			}
			next := ""
			truncated := start+1 < len(keys)
			if truncated {
				next = "<NextContinuationToken>" + strconv.Itoa(start+1) + "</NextContinuationToken>"
			}
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Prefix>%s</Prefix><MaxKeys>1</MaxKeys><IsTruncated>%t</IsTruncated>%s%s</ListBucketResult>`, bucket, prefix, truncated, next, page.String())
			return
		}
		content, ok := objects[strings.TrimPrefix(path, "/")]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		fmt.Fprint(w, content)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestS3ListAndGet(t *testing.T) {
	objects := map[string]string{
		"imported/aws/eu-west-2/terraform.tfstate": state,
		"imported/aws/global/terraform.tfstate":    `{"version": 4}`,
		"imported/notes.txt":                       "not state",
		"other/terraform.tfstate":                  `{"version": 4}`,
	}
	server := fakeS3(t, "acme-state", objects)
	store := &S3{client: s3.New(s3.Options{
		Region:       "eu-west-2",
		BaseEndpoint: aws.String(server.URL),
		UsePathStyle: true,
		Credentials:  aws.AnonymousCredentials{},
	})}
	ctx := context.Background()

	// One key a page: every page is read.
	keys, err := store.List(ctx, "acme-state", "imported/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"imported/aws/eu-west-2/terraform.tfstate", "imported/aws/global/terraform.tfstate", "imported/notes.txt"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("List: got %q, want %q", keys, want)
	}
	if keys, err := store.List(ctx, "acme-state", "none/"); err != nil || len(keys) != 0 {
		t.Errorf("List of an empty prefix: got %q, %v", keys, err)
	}

	content, err := store.Get(ctx, "acme-state", "imported/aws/eu-west-2/terraform.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != state {
		t.Errorf("Get: got %q", content)
	}
	if _, err := store.Get(ctx, "acme-state", "imported/missing.tfstate"); err == nil {
		t.Error("want an error for a key that doesn't exist")
	}
}

// NewS3 takes the region from ?region= first, then from the profile, and
// fails for a profile the shared config doesn't have.
func TestNewS3(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, []byte("[profile state]\nregion = us-east-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(dir, "credentials")
	if err := os.WriteFile(credentials, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", config)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)
	for _, name := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx := context.Background()

	for _, tc := range []struct{ region, profile, want string }{
		{"eu-west-1", "", "eu-west-1"},
		{"", "state", "us-east-2"},
		{"eu-west-1", "state", "eu-west-1"},
	} {
		store, err := NewS3(ctx, tc.region, tc.profile)
		if err != nil {
			t.Errorf("NewS3(%q, %q): %v", tc.region, tc.profile, err)
			continue
		}
		if got := store.(*S3).client.Options().Region; got != tc.want {
			t.Errorf("NewS3(%q, %q): region %q, want %q", tc.region, tc.profile, got, tc.want)
		}
	}
	if _, err := NewS3(ctx, "", "missing"); err == nil {
		t.Error("want an error for a profile the shared config doesn't have")
	}
}
