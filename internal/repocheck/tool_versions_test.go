// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const toolVersionsScript = ".github/scripts/tool-versions.sh"

// toolVersionFiles are the files tool-versions.sh reads the pins from.
var toolVersionFiles = []string{
	".github/workflows/release.yml",
	".github/workflows/test.yml",
	".github/actions/setup-engines/action.yml",
	".mega-linter.yml",
	".github/workflows/megalinter.yml",
	".github/scripts/govulncheck-gate.sh",
	".github/workflows/remote.yml",
	"scripts/fast-checks.sh",
	"go.mod",
}

// TestToolVersions checks that tool-versions.sh still finds every pin it
// reads, in the repository's own files, and lists a tool only when its
// latest release differs. The lookups are faked: every release is 9.9.9,
// but the Go toolchain's, which is what go.mod pins.
func TestToolVersions(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the tool versions test needs jq")
	}
	repo := newRepositoryFixture(t)
	repo.install(toolVersionsScript)
	for _, f := range toolVersionFiles {
		repo.install(f)
	}
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var toolchain string
	for _, line := range strings.Split(string(mod), "\n") {
		if v, ok := strings.CutPrefix(line, "toolchain "); ok {
			toolchain = v
		}
	}
	bin := t.TempDir()
	for name, script := range map[string]string{
		"gh": "#!/bin/sh\necho v9.9.9\n",
		"curl": `#!/bin/sh
for arg; do url=$arg; done
case $url in
  *go.dev*) echo '[{"version": "` + toolchain + `"}]' ;;
  *proxy.golang.org*) echo '{"Version": "v9.9.9"}' ;;
  *) echo '{"version": "9.9.9"}' ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo.env = append(repo.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := repo.commandAt(repo.dir, 0, "bash", toolVersionsScript)

	for _, tool := range []string{"GoReleaser", "conftest", "TFLint", "Trivy", "golangci-lint", "MegaLinter", "govulncheck", "benchstat", "markdownlint-cli2"} {
		if !strings.Contains(out, "| "+tool+" | ") {
			t.Errorf("no row for %s:\n%s", tool, out)
		}
	}
	if strings.Contains(out, "not found") || strings.Contains(out, "couldn't be looked up") {
		t.Errorf("a pin not found or a lookup failed:\n%s", out)
	}
	if strings.Contains(out, "Go toolchain") {
		t.Errorf("the Go toolchain is current, but listed:\n%s", out)
	}
}
