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
  */actions/runs/*/jobs*) id=${path#*/runs/}; file=$dir/jobs-${id%%/*}.json ;;
  *head_sha=*) sha=${path##*head_sha=}; file=$dir/runs-${sha%%&*}.json ;;
  *) echo "unexpected call: $path" >&2; exit 2 ;;
esac
[ -f "$file" ] || echo '{"workflow_runs": [], "jobs": []}' >"$file"
jq -r "$filter" "$file"
`

// gateJob is a job of a Tests run.
type gateJob struct {
	Name       string  `json:"name"`
	Conclusion *string `json:"conclusion"`
}

// e2eJobs are the e2e jobs of a Tests run, each cloud's with the
// conclusion given for it ("" for none of its jobs, which a run that never
// queued them has).
func e2eJobs(aws, google, azure string) []gateJob {
	var jobs []gateJob
	for cloud, conclusion := range map[string]string{"aws": aws, "google": google, "azure": azure} {
		if conclusion == "" {
			continue
		}
		for _, engine := range []string{"terraform", "opentofu"} {
			c := conclusion
			jobs = append(jobs, gateJob{Name: "e2e-" + cloud + " (" + engine + ", round-trip)", Conclusion: &c})
		}
	}
	// The jobs that gather the others and the unit jobs aren't e2e ones.
	skipped := "skipped"
	return append(jobs, gateJob{Name: "e2e", Conclusion: &skipped}, gateJob{Name: "go test", Conclusion: &skipped})
}

type gateRun struct {
	ID         int     `json:"id"`
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
		// releaseJobs and headJobs are the jobs of the Tests run on each
		// commit, all the e2e ones succeeded unless set.
		releaseJobs, headJobs []gateJob
		merged                bool
		status                int
		want                  string
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
		{
			name:        "the e2e jobs were skipped on the release commit and its pull request",
			release:     append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			releaseJobs: e2eJobs("skipped", "skipped", "skipped"),
			status:      1,
			want:        "::error::Tests hasn't run the e2e-aws jobs to success",
		},
		{
			name:        "the e2e jobs of one cloud failed",
			release:     append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			releaseJobs: e2eJobs("success", "failure", "success"),
			status:      1,
			want:        "e2e-google: failure on the release commit, not run on the pull request",
		},
		{
			name:        "the e2e jobs of one cloud never ran",
			release:     append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			releaseJobs: e2eJobs("success", "success", ""),
			status:      1,
			want:        "::error::Tests hasn't run the e2e-azure jobs to success",
		},
		{
			name:        "the e2e jobs passed on the pull request alone",
			release:     append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			releaseJobs: e2eJobs("skipped", "skipped", "skipped"),
			head:        []gateRun{run("Tests", "success", "2026-11-09T09:00:00Z"), run("MegaLinter", "success", "2026-11-09T09:00:00Z")},
			headJobs:    e2eJobs("success", "success", "success"),
			merged:      true,
			want:        "e2e-azure: skipped on the release commit, success on the pull request",
		},
		{
			name:        "each cloud's e2e jobs passed on one of the two",
			release:     append([]gateRun{run("Tests", "success", "2026-11-10T09:00:00Z"), run("MegaLinter", "success", "2026-11-10T09:00:00Z")}, sandboxes...),
			releaseJobs: e2eJobs("success", "skipped", "skipped"),
			head:        []gateRun{run("Tests", "success", "2026-11-09T09:00:00Z"), run("MegaLinter", "success", "2026-11-09T09:00:00Z")},
			headJobs:    e2eJobs("skipped", "success", "success"),
			merged:      true,
			want:        "e2e-google: skipped on the release commit, success on the pull request",
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
			// The runs on the release commit have the ID 1, those on the pull
			// request the ID 2, which the fake gh serves the jobs of.
			withID := func(runs []gateRun, id int) []gateRun {
				out := append([]gateRun{}, runs...)
				for i := range out {
					out[i].ID = id
				}
				return out
			}
			allPassed := e2eJobs("success", "success", "success")
			if tt.releaseJobs == nil {
				tt.releaseJobs = allPassed
			}
			if tt.headJobs == nil {
				tt.headJobs = allPassed
			}
			writeJSON("runs-release.json", map[string]any{"workflow_runs": withID(tt.release, 1)})
			writeJSON("runs-head.json", map[string]any{"workflow_runs": withID(tt.head, 2)})
			writeJSON("jobs-1.json", map[string]any{"jobs": tt.releaseJobs})
			writeJSON("jobs-2.json", map[string]any{"jobs": tt.headJobs})
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
