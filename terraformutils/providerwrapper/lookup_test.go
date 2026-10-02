package providerwrapper //nolint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryLookupPicksHighestVersion(t *testing.T) {
	prefix := t.TempDir()
	awsDir := filepath.Join(prefix, "providers", "registry.terraform.io", "hashicorp", "aws")
	// Directory order is lexical, so 3.9.0 sorts after 3.10.0.
	for _, v := range []string{"3.2.0", "3.10.0", "3.9.0"} {
		touch(t, filepath.Join(awsDir, v, pluginMachineName, "terraform-provider-aws_v"+v+"_x5"))
	}
	// Highest version, but not built for this platform.
	touch(t, filepath.Join(awsDir, "4.0.0", "plan9_mips", "terraform-provider-aws_v4.0.0_x5"))

	got, err := getProviderFileNameV13andV14(prefix, "aws")
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(awsDir, "3.10.0", pluginMachineName, "terraform-provider-aws_v3.10.0_x5")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestRegistryLookupFallsBackToV13Layout(t *testing.T) {
	prefix := t.TempDir()
	binary := filepath.Join(prefix, "plugins", "registry.terraform.io", "hashicorp", "google", "4.0.0", pluginMachineName, "terraform-provider-google_v4.0.0_x5")
	touch(t, binary)

	got, err := getProviderFileNameV13andV14(prefix, "google")
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Errorf("got %s, want %s", got, binary)
	}
}

func TestFindProviderBinary(t *testing.T) {
	tests := map[string]struct {
		files []string
		want  string
	}{
		"highest version, not last in directory order": {
			files: []string{"terraform-provider-aws_v3.10.0_x5", "terraform-provider-aws_v3.9.0_x5"},
			want:  "terraform-provider-aws_v3.10.0_x5",
		},
		"ignores providers sharing the name prefix": {
			files: []string{"terraform-provider-aws_v3.0.0_x5", "terraform-provider-awscc_v9.0.0_x5"},
			want:  "terraform-provider-aws_v3.0.0_x5",
		},
		"windows executables": {
			files: []string{"terraform-provider-aws_v3.0.0_x5.exe", "terraform-provider-aws_v3.1.0_x5.exe"},
			want:  "terraform-provider-aws_v3.1.0_x5.exe",
		},
		"versioned beats unversioned": {
			files: []string{"terraform-provider-aws", "terraform-provider-aws_v1.0.0"},
			want:  "terraform-provider-aws_v1.0.0",
		},
		"unversioned only": {
			files: []string{"terraform-provider-aws"},
			want:  "terraform-provider-aws",
		},
		"no match": {
			files: []string{"terraform-provider-awscc_v1.0.0"},
			want:  "",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				touch(t, filepath.Join(dir, f))
			}

			got := findProviderBinary(dir, "aws")

			want := ""
			if tc.want != "" {
				want = filepath.Join(dir, tc.want)
			}
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestExplainHandshakeError(t *testing.T) {
	mismatch := errors.New("Incompatible API version with plugin. Plugin version: 6, Client versions: [5]")

	err := explainHandshakeError("cloudflare", "/p/terraform-provider-cloudflare_v4.0.0", mismatch)

	if !errors.Is(err, mismatch) {
		t.Error("want the original error wrapped")
	}
	for _, want := range []string{"cloudflare", "only protocol 5", "Plugin version: 6"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	other := errors.New("exec format error")
	if got := explainHandshakeError("aws", "/p/x", other); got != other {
		t.Errorf("other errors must pass through unchanged, got %v", got)
	}
}
