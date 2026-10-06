// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"encoding/base64"
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
