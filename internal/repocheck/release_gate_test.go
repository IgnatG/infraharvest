// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseGateScript = ".github/scripts/release-gate.sh"

// fakeGH answers the two API calls release-gate.sh makes from JSON files in
// its directory, applying the call's --jq filter as gh does.
const fakeGH = `#!/usr/bin/env bash
set -euo pipefail
dir=$(dirname "$0")
path= filter=
while [ $# -gt 0 ]; do
  case $1 in
    api|--paginate) ;;
    --jq) filter=$2; shift ;;
    *) path=$1 ;;
  esac
  shift
done
case $path in
  */pulls) file=$dir/pulls.json ;;
  *head_sha=*) sha=${path##*head_sha=}; file=$dir/runs-${sha%%&*}.json ;;
  *) echo "unexpected call: $path" >&2; exit 2 ;;
esac
[ -f "$file" ] || echo '{"workflow_runs": []}' >"$file"
jq -r "$filter" "$file"
`

type gateRun struct {
	Name       string  `json:"name"`
	Conclusion *string `json:"conclusion"`
	CreatedAt  string  `json:"created_at"`
}

func run(name, conclusion, at string) gateRun {
	r := gateRun{Name: name, CreatedAt: at}
	if conclusion != "" {
		r.Conclusion = &conclusion
	}
	return r
}

// TestReleaseGate checks which workflow runs on the release commit, and on
// the last commit of the pull request it merged, let a release go ahead.
func TestReleaseGate(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the release gate's test needs jq")
	}
	sandboxes := []gateRun{
		run("Sandbox (AWS)", "success", "2026-11-10T10:00:00Z"),
		run("Sandbox (Azure)", "success", "2026-11-10T10:00:00Z"),
		run("Sandbox (Google Cloud)", "success", "2026-11-10T10:00:00Z"),
	}
	for _, tt := range []struct {
		name          string
		release, head []gateRun
		merged        bool
		status        int
		want          string
	}{
		{
			name:    "everything passed on the release commit",
			release: append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			want:    "Tests: success on the release commit",
		},
		{
			name:    "the tests passed on the pull request",
			release: sandboxes,
			head:    []gateRun{run("Tests", "success", "2026-11-09T09:00:00Z"), run("MegaLinter", "success", "2026-11-09T09:00:00Z")},
			merged:  true,
			want:    "Tests: none on the release commit, success on the pull request",
		},
		{
			name:    "a later run failed",
			release: sandboxes,
			head: []gateRun{
				run("Tests", "success", "2026-11-09T09:00:00Z"), run("Tests", "failure", "2026-11-09T11:00:00Z"),
				run("MegaLinter", "success", "2026-11-09T09:00:00Z"),
			},
			merged: true,
			status: 1,
			want:   "::error::Tests hasn't passed",
		},
		{
			name:    "a failure that a later run passes",
			release: sandboxes,
			head: []gateRun{
				run("Tests", "failure", "2026-11-09T09:00:00Z"), run("Tests", "success", "2026-11-09T11:00:00Z"),
				run("MegaLinter", "success", "2026-11-09T09:00:00Z"),
			},
			merged: true,
			want:   "Tests: none on the release commit, success on the pull request",
		},
		{
			name:    "a run still going",
			release: append([]gateRun{run("Tests", "", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			status:  1,
			want:    "Tests: pending on the release commit",
		},
		{
			name:    "a sandbox not run",
			release: append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes[:2]...),
			status:  1,
			want:    "::error::Sandbox (Google Cloud) hasn't passed",
		},
		{
			name:   "a sandbox passed only on the pull request",
			head:   append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			merged: true,
			status: 1,
			want:   "::error::Sandbox (AWS) hasn't passed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(releaseGateScript)
			bin := t.TempDir()
			writeJSON := func(name string, v any) {
				t.Helper()
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, name), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
				t.Fatal(err)
			}
			writeJSON("runs-release.json", map[string]any{"workflow_runs": append([]gateRun{}, tt.release...)})
			writeJSON("runs-head.json", map[string]any{"workflow_runs": append([]gateRun{}, tt.head...)})
			pulls := []map[string]any{}
			if tt.merged {
				pulls = append(pulls, map[string]any{"merged_at": "2026-11-09T12:00:00Z", "head": map[string]string{"sha": "head"}})
			}
			writeJSON("pulls.json", pulls)

			env := repo.env
			repo.env = append(append([]string(nil), env...), "GH_REPO=o/r", "SHA=release", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			out := repo.runScript(releaseGateScript, tt.status, tt.want)
			repo.env = env
			if tt.status == 0 && strings.Contains(out, "::error::") {
				t.Errorf("passed with errors:\n%s", out)
			}
		})
	}
}
