// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// discoverAzureErr runs infraharvest discover azure with args alone and
// returns its error, which the test expects.
func discoverAzureErr(ctx context.Context, t *testing.T, args ...string) error {
	t.Helper()
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{"discover", "azure", "--pick=false", "--out=" + t.TempDir(), "--selection=" + t.TempDir() + "/selection.yaml"}, args...))
	return root.ExecuteContext(ctx)
}

// TestAzureManagedStateSources checks --managed-state with Azure Blob
// Storage URLs. floci-az has no storage, and a URL on blob.core.windows.net
// would be read from Azure itself, so the test checks what the command
// refuses from the URL alone, before it signs in or lists anything: a URL
// that names no container, and one on a host that isn't Azure's Blob
// Storage, to which the credentials of the backend would otherwise go. The
// error names the source and what to write.
func TestAzureManagedStateSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	for _, tc := range []struct{ source, want string }{
		{"https://e2eaccount.blob.core.windows.net/", "name a container: https://<account>.blob.core.windows.net/<container>/<prefix>"},
		{"https://e2eaccount.blob.core.windows.net", "name a container"},
		{"https://e2eaccount.example.com/state/prod", "not a Blob Storage URL"},
	} {
		err := discoverAzureErr(ctx, t, "--subscriptions=00000000-0000-0000-0000-000000000001", "--services=resource_group", "--managed-state="+tc.source)
		if err == nil || !strings.Contains(err.Error(), "state "+tc.source) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("--managed-state=%s: %v, want an error naming the source and %q", tc.source, err, tc.want)
		}
	}
}

// TestAzureManagementGroup checks --management-group, which imports every
// enabled subscription under a management group, found through Resource
// Graph, instead of --subscriptions: floci-az answers with its one
// subscription, whatever the group, and discover lists the resources of
// testdata/azure in it, which it signs in to the subscription of. A group
// ID with characters Azure doesn't allow is refused, before any request.
func TestAzureManagementGroup(t *testing.T) {
	e := seedAzure(t)
	out, path := t.TempDir(), filepath.Join(t.TempDir(), "selection.yaml")
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"discover", "azure", "--pick=false", "--out=" + out, "--selection=" + path, "--allow-partial",
		"--management-group=e2e-group", "--resource-group=" + azureResourceGroup, "--services=" + strings.Join(azureServices, ","),
	})
	logged := logDuring(func() {
		// The tags, which only Resource Graph reads, fail on the emulator
		// (see azureEmulatorFailure): --allow-partial.
		if err := root.ExecuteContext(e.ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
			t.Errorf("infraharvest discover azure --management-group: %v", err)
		}
	})
	if !strings.Contains(logged, "1 subscriptions under management group e2e-group") {
		t.Errorf("discover didn't read the subscriptions of the management group; the log:\n%s", logged)
	}
	checkDiscovered(t, path, out, "azurerm", e.state)

	err := discoverAzureErr(e.ctx, t, "--management-group=e2e/group", "--services=resource_group")
	if err == nil || !strings.Contains(err.Error(), "characters Azure doesn't allow") {
		t.Errorf("--management-group=e2e/group: %v, want it refused for its characters", err)
	}
}
