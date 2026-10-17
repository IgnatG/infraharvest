// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAzureBlobClient(t *testing.T) {
	const serviceURL = "https://acmestate.blob.core.windows.net/"
	key := base64.StdEncoding.EncodeToString([]byte("not a real key"))
	for name, env := range map[string]map[string]string{
		"access key":        {"ARM_ACCESS_KEY": key},
		"SAS token":         {"ARM_SAS_TOKEN": "?sv=2022-11-02&sig=x"},
		"service principal": {"ARM_CLIENT_ID": "client", "ARM_CLIENT_SECRET": "secret", "ARM_TENANT_ID": "tenant"},
	} {
		client, err := azureBlobClient(serviceURL, func(k string) string { return env[k] })
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.HasPrefix(client.URL(), serviceURL) {
			t.Errorf("%s: URL %q", name, client.URL())
		}
	}
	if _, err := azureBlobClient(serviceURL, func(k string) string {
		if k == "ARM_ACCESS_KEY" {
			return "not base64!"
		}
		return ""
	}); err == nil {
		t.Error("want an error for an access key that isn't base64")
	}
}

// fakeBlob serves one container's blobs: List Blobs, a blob a page with
// NextMarker, and Get Blob.
func fakeBlob(t *testing.T, container string, blobs map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/"+container)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/"+container) {
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusBadRequest)
			return
		}
		query := r.URL.Query()
		if path == "" || path == "/" {
			if query.Get("restype") != "container" || query.Get("comp") != "list" {
				http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
				return
			}
			prefix := query.Get("prefix")
			var names []string
			for name := range blobs {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			start := 0
			if marker := query.Get("marker"); marker != "" {
				start, _ = strconv.Atoi(marker)
			}
			page, next := "", ""
			if start < len(names) {
				page = "<Blob><Name>" + names[start] + "</Name><Properties></Properties></Blob>"
			}
			if start+1 < len(names) {
				next = strconv.Itoa(start + 1)
			}
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ContainerName="%s"><Prefix>%s</Prefix><MaxResults>1</MaxResults><Blobs>%s</Blobs><NextMarker>%s</NextMarker></EnumerationResults>`, container, prefix, page, next)
			return
		}
		content, ok := blobs[strings.TrimPrefix(path, "/")]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.Header().Set("x-ms-error-code", "BlobNotFound")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<Error><Code>BlobNotFound</Code><Message>The specified blob does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		fmt.Fprint(w, content)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAzureBlobListAndGet(t *testing.T) {
	blobs := map[string]string{
		"imported/azurerm/uksouth/terraform.tfstate": state,
		"imported/azurerm/global/terraform.tfstate":  `{"version": 4}`,
		"imported/notes.txt":                         "not state",
		"other/terraform.tfstate":                    `{"version": 4}`,
	}
	server := fakeBlob(t, "tfstate", blobs)
	client, err := azureBlobClient(server.URL+"/", func(k string) string {
		if k == "ARM_SAS_TOKEN" {
			return "sv=2022-11-02&sig=x"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &AzureBlob{client: client}
	ctx := context.Background()

	// One blob a page: every page is read, under the prefix only.
	names, err := store.List(ctx, "tfstate", "imported/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"imported/azurerm/global/terraform.tfstate", "imported/azurerm/uksouth/terraform.tfstate", "imported/notes.txt"}; !reflect.DeepEqual(names, want) {
		t.Errorf("List: got %q, want %q", names, want)
	}
	if names, err := store.List(ctx, "tfstate", "none/"); err != nil || len(names) != 0 {
		t.Errorf("List of an empty prefix: got %q, %v", names, err)
	}

	content, err := store.Get(ctx, "tfstate", "imported/azurerm/uksouth/terraform.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != state {
		t.Errorf("Get: got %q", content)
	}
	if _, err := store.Get(ctx, "tfstate", "imported/missing.tfstate"); err == nil {
		t.Error("want an error for a blob that doesn't exist")
	}
}
