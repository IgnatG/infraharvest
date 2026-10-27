// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"strings"
	"testing"
)

// The fixtures' licence lines are test data, not this file's licence:
// REUSE skips them.
// REUSE-IgnoreStart

// These tests run the scripts against real Git repositories, as CI and the
// pre-commit hook do. Run with: go test ./internal/repocheck -timeout 60s
const (
	headersScript = "scripts/check-headers.sh"
	namesScript   = "scripts/check-names.sh"
	// fastChecksScript is what the pre-commit hook runs.
	fastChecksScript = "scripts/fast-checks.sh"
	licensedFile     = "// Copyright 2026 Example\n// SPDX-License-Identifier: AGPL-3.0-only\n\npackage fixture\n"
	headersPass      = "every Go file, shell script and workflow carries"
	namesPass        = "no tracked file uses"
)

func TestCheckHeadersClassification(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		ok      bool
	}{
		{"header", licensedFile, true},
		{"licence line only", "// SPDX-License-Identifier: AGPL-3.0-only\npackage fixture\n", true},
		{"no header", "package fixture\n", false},
		{"empty file", "", false},
		{"other licence", "// SPDX-License-Identifier: Apache-2.0\npackage fixture\n", false},
		{"near match", "// SPDX-License-Identifier: AGPL-3x0-only\npackage fixture\n", false},
		{"not at the start of a line", "package fixture\n\nconst s = \"// SPDX-License-Identifier: AGPL-3.0-only\"\n", false},
	} {
		// REUSE-IgnoreEnd
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(headersScript)
			repo.track("internal/example.go", tt.content)
			// Only Go files need the header.
			repo.track("notes.txt", "no header\n")
			if tt.ok {
				repo.runScript(headersScript, 0, headersPass)
			} else {
				repo.runScript(headersScript, 1, "::error file=internal/example.go::internal/example.go lacks the licence header")
			}
		})
	}
}

// REUSE-IgnoreStart

func TestCheckHeadersShellScriptsAndWorkflows(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    string
		content string
		ok      bool
	}{
		{"script", "scripts/example.sh", "#!/usr/bin/env bash\n# Copyright 2026 Example\n# SPDX-License-Identifier: AGPL-3.0-only\n", true},
		{"script with no header", "scripts/example.sh", "#!/usr/bin/env bash\necho hi\n", false},
		{"script with a Go comment", "scripts/example.sh", "// SPDX-License-Identifier: AGPL-3.0-only\n", false},
		{"workflow", ".github/workflows/example.yml", "# Copyright 2026 Example\n# SPDX-License-Identifier: AGPL-3.0-only\n\nname: Example\n", true},
		{"workflow with no header", ".github/workflows/example.yml", "name: Example\n", false},
	} {
		// REUSE-IgnoreEnd
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(headersScript)
			repo.track(tt.path, tt.content)
			// Other YAML files need no header.
			repo.track(".github/dependabot.yml", "version: 2\n")
			if tt.ok {
				repo.runScript(headersScript, 0, headersPass)
				repo.runScript(headersScript, 0, headersPass, tt.path)
			} else {
				want := "::error file=" + tt.path + "::" + tt.path + " lacks the licence header. It starts with \"# Copyright"
				repo.runScript(headersScript, 1, want)
				repo.runScript(headersScript, 1, want, tt.path)
			}
		})
	}
}

func TestCheckHeadersReportsEveryFile(t *testing.T) {
	repo := newRepositoryFixture(t)
	repo.install(headersScript)
	repo.track("a.go", "package fixture\n")
	repo.track("b/c.go", "package fixture\n")
	repo.track("own.go", licensedFile)
	// Untracked files aren't checked.
	repo.write("untracked.go", "package fixture\n")
	output := repo.runScript(headersScript, 1, "::error file=a.go::")
	if !strings.Contains(output, "::error file=b/c.go::") {
		t.Errorf("missing b/c.go in:\n%s", output)
	}
	if got := strings.Count(output, "::error"); got != 2 {
		t.Errorf("error count = %d, want 2:\n%s", got, output)
	}
}

func TestCheckHeadersCachedAndFiles(t *testing.T) {
	repo := newRepositoryFixture(t)
	repo.install(headersScript)
	repo.track("example.go", licensedFile)
	repo.track("other.go", "package fixture\n")
	// The index keeps the header; the working tree drops it.
	repo.write("example.go", "package fixture\n")

	repo.runScript(headersScript, 1, "::error file=example.go::", "example.go")
	repo.runScript(headersScript, 0, headersPass, "--cached", "example.go")
	repo.runScript(headersScript, 0, headersPass, "--cached", "--", "example.go")
	repo.runScript(headersScript, 1, "::error file=other.go::", "--cached")
	repo.runScript(headersScript, 0, "no Go files, shell scripts or workflows to check", "--cached", "notes.md")
}

// REUSE-IgnoreStart

// The pre-commit hook checks the headers of the shell scripts and workflows
// a commit stages, not only of its Go files, as they are staged.
func TestFastChecksStagedHeaders(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    string
		content string
		ok      bool
	}{
		{"script", "scripts/example.sh", "#!/usr/bin/env bash\n# Copyright 2026 Example\n# SPDX-License-Identifier: AGPL-3.0-only\n", true},
		{"script with no header", "scripts/example.sh", "#!/usr/bin/env bash\necho hi\n", false},
		{"workflow", ".github/workflows/example.yml", "# Copyright 2026 Example\n# SPDX-License-Identifier: AGPL-3.0-only\n\nname: Example\n", true},
		{"workflow with no header", ".github/workflows/example.yml", "name: Example\n", false},
	} {
		// REUSE-IgnoreEnd
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(fastChecksScript)
			repo.install(headersScript)
			repo.track(tt.path, tt.content)
			// The hook runs in the repository, which the script finds
			// from where it runs.
			staged := func(wantStatus int, wantOutput string) {
				t.Helper()
				output := repo.commandAt(repo.dir, wantStatus, "bash", fastChecksScript, "--staged", "--only", "headers")
				if !strings.Contains(output, wantOutput) {
					t.Fatalf("output must contain %q:\n%s", wantOutput, output)
				}
			}
			if !tt.ok {
				staged(1, "::error file="+tt.path+"::")
				return
			}
			staged(0, "ok    headers (3 files)")
			// The index's header counts, not the working tree's.
			repo.write(tt.path, "echo hi\n")
			staged(0, "ok    headers (3 files)")
		})
	}
}

// The names are built from pieces, as the script builds them, so that this
// file doesn't match them.
var guardedNames = []string{
	"Terra" + "former",
	"TERRA" + "FORMER",
	"tf" + "er--example",
	"TF" + "ER--example",
	"terraform" + "utils",
	"github.com/example/terraform" + "utils/x",
}

func TestCheckNamesFindsEveryName(t *testing.T) {
	for _, name := range guardedNames {
		t.Run(name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(namesScript)
			repo.track("docs/page.md", "# Page\n\nSee "+name+".\n")
			repo.runScript(namesScript, 1, "::error file=docs/page.md,line=3::")
		})
	}
}

func TestCheckNamesPasses(t *testing.T) {
	repo := newRepositoryFixture(t)
	// The script is tracked too, and must not match itself.
	repo.install(namesScript)
	repo.track("docs/page.md", "terraform import, tfer-label, a former name\n")
	// Binary files and untracked files aren't checked.
	repo.track("blob.bin", "\x00"+guardedNames[0])
	repo.write("untracked.md", guardedNames[0]+"\n")
	repo.runScript(namesScript, 0, namesPass)
}

func TestCheckNamesAllowlist(t *testing.T) {
	repo := newRepositoryFixture(t)
	repo.install(namesScript)
	repo.track("docs/allowed.md", guardedNames[0]+"\n")
	repo.track("docs/glob/one.md", guardedNames[2]+"\n")
	repo.track("docs/other.md", guardedNames[4]+"\n")
	repo.write(".github/names-allowlist.txt", "# why each entry is here\n\ndocs/allowed.md\ndocs/glob/*\n")

	output := repo.runScript(namesScript, 1, "::error file=docs/other.md,line=1::")
	if got := strings.Count(output, "::error"); got != 1 {
		t.Errorf("error count = %d, want 1 (the allowlist covers the others):\n%s", got, output)
	}
	repo.git(0, "rm", "--quiet", "--force", "--", "docs/other.md")
	repo.runScript(namesScript, 0, namesPass)
}

func TestCheckNamesCachedAndFiles(t *testing.T) {
	repo := newRepositoryFixture(t)
	repo.install(namesScript)
	repo.track("a.md", "clean\n")
	repo.track("b.md", guardedNames[0]+"\n")
	// The working tree adds a name the index doesn't have.
	repo.write("a.md", guardedNames[0]+"\n")

	repo.runScript(namesScript, 1, "::error file=a.md,line=1::", "a.md")
	repo.runScript(namesScript, 0, namesPass, "--cached", "a.md")
	repo.runScript(namesScript, 0, namesPass, "--cached", "--", "a.md")
	repo.runScript(namesScript, 1, "::error file=b.md,line=1::", "--cached")
}
