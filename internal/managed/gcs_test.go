// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fakeGCS serves one bucket's objects as Cloud Storage does to a client
// pointed at an emulator: objects.list on the JSON API, an object a page,
// and reads on the XML API.
func fakeGCS(t *testing.T, bucket string, objects map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/storage/v1/b/"+bucket+"/o" {
			prefix := r.URL.Query().Get("prefix")
			var names []string
			for name := range objects {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			start := 0
			if token := r.URL.Query().Get("pageToken"); token != "" {
				start, _ = strconv.Atoi(token)
			}
			page := map[string]any{"kind": "storage#objects"}
			if start < len(names) {
				page["items"] = []map[string]string{{"kind": "storage#object", "bucket": bucket, "name": names[start]}}
			}
			if start+1 < len(names) {
				page["nextPageToken"] = strconv.Itoa(start + 1)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(page); err != nil {
				t.Error(err)
			}
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/"+bucket+"/")
		content, found := objects[name]
		if !ok || !found {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGCSListAndGet(t *testing.T) {
	objects := map[string]string{
		"roots/aws/global/default.tfstate":    state,
		"roots/aws/eu-west-2/default.tfstate": `{"version": 4}`,
		"roots/README.md":                     "not state",
		"other/default.tfstate":               `{"version": 4}`,
	}
	server := fakeGCS(t, "acme-state", objects)
	// NewGCS reads from an emulator, without credentials, as Terraform's
	// gcs backend does.
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	// No background bucket lookups for tracing.
	t.Setenv("GO_OTEL_BUCKETMETADATA_DISABLED", "true")
	ctx := context.Background()
	store, err := NewGCS(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// An object a page: every page is read.
	names, err := store.List(ctx, "acme-state", "roots/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"roots/README.md", "roots/aws/eu-west-2/default.tfstate", "roots/aws/global/default.tfstate"}; !reflect.DeepEqual(names, want) {
		t.Errorf("List: got %q, want %q", names, want)
	}
	if names, err := store.List(ctx, "acme-state", "none/"); err != nil || len(names) != 0 {
		t.Errorf("List of an empty prefix: got %q, %v", names, err)
	}

	content, err := store.Get(ctx, "acme-state", "roots/aws/global/default.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != state {
		t.Errorf("Get: got %q", content)
	}
	if _, err := store.Get(ctx, "acme-state", "roots/missing.tfstate"); err == nil {
		t.Error("want an error for an object that doesn't exist")
	}
}
