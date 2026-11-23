// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package action

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// failingBinary stands in for infraharvest failing before it wrote any
// report; writingBinary for an import that writes its report and succeeds.
const (
	failingBinary = "#!/usr/bin/env bash\necho 'failed before writing a report' >&2\nexit 1\n"
	writingBinary = "#!/usr/bin/env bash\nprintf '{\"fresh\":true}' > out/_reports/report.json\n"
)

// runStep runs the script of the action step called name, as the runner does
// (bash -e), in a new directory that has the output directory out with the
// files of reports in its report directory, written as "stale", and the
// fake binary. It returns the directory, the step's outcome and its output.
func runStep(t *testing.T, name string, binary string, reports map[string]string, env map[string]string) (dir, out string, err error) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s isn't installed", tool)
		}
	}
	script, _ := stepOf(t, name)
	dir = t.TempDir()
	reportDir := filepath.Join(dir, "out", "_reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, content := range reports {
		if err := os.WriteFile(filepath.Join(reportDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin", "infraharvest")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	actionPath, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	// What Check the import wrote for the Import step: one argument a line,
	// ended by a NUL.
	if err := os.WriteFile(filepath.Join(dir, "infraharvest-import-args"), []byte("import\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GITHUB_ACTION_PATH=" + actionPath,
		"GITHUB_OUTPUT=" + filepath.Join(dir, "github-output"),
		"GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary"),
		"RUNNER_TEMP=" + dir,
		"BINARY=" + bin,
		"OUT=out",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	return dir, buf.String(), err
}

// The steps that write a report remove the one an earlier run left, such as
// one a pull request committed, before they run: a step that fails before it
// writes its own leaves none, so that the Control plane step has nothing of
// an older run to send as this one's.
func TestStepsRemoveTheReportOfAnEarlierRun(t *testing.T) {
	for _, tc := range []struct {
		step, file string
		env        map[string]string
	}{
		{"Import", "report.json", map[string]string{"ALLOW_PARTIAL": "false"}},
		{"Drift", "drift.json", map[string]string{"DRIFT": "report", "ENGINE": "", "CONFIG": "", "DRIFT_DECISIONS": "", "DRIFT_ACCEPT": "", "ENVIRONMENT": ""}},
		{"Verify", "verify.json", map[string]string{"ENGINE": "", "CONFIG": ""}},
	} {
		t.Run(tc.step, func(t *testing.T) {
			// Other reports, of the steps that didn't run, stay.
			others := map[string]string{"report.json": "stale", "drift.json": "stale", "verify.json": "stale"}
			dir, out, err := runStep(t, tc.step, failingBinary, others, tc.env)
			if err == nil {
				t.Fatalf("the step passed although infraharvest failed:\n%s", out)
			}
			reports := filepath.Join(dir, "out", "_reports")
			if _, err := os.Stat(filepath.Join(reports, tc.file)); !os.IsNotExist(err) {
				t.Errorf("%s of the earlier run is still there after the step failed: %v", tc.file, err)
			}
			for file := range others {
				if file == tc.file {
					continue
				}
				if _, err := os.Stat(filepath.Join(reports, file)); err != nil {
					t.Errorf("the %s step removed %s: %v", tc.step, file, err)
				}
			}
		})
	}
}

// What a step that ran writes is kept.
func TestStepsKeepTheReportTheyWrote(t *testing.T) {
	env := map[string]string{"ALLOW_PARTIAL": "false"}
	dir, out, err := runStep(t, "Import", writingBinary, map[string]string{"report.json": "stale"}, env)
	if err != nil {
		t.Fatalf("the step failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out", "_reports", "report.json"))
	if err != nil || string(got) != `{"fresh":true}` {
		t.Errorf("report.json: %q, %v; want the one the step wrote", got, err)
	}
}

// A report directory the repository links elsewhere is left alone: nothing
// outside the repository is removed, and the Control plane step refuses the
// link as it did.
func TestStepsLeaveALinkedReportDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(elsewhere, "report.json")
	if err := os.WriteFile(keep, []byte("not the repository's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "out", "_reports")); err != nil {
		t.Fatal(err)
	}
	script := `source "$GITHUB_ACTION_PATH/output.sh"; discard "$OUT/_reports/report.json"`
	actionPath, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_ACTION_PATH=" + actionPath, "GITHUB_OUTPUT=" + filepath.Join(dir, "o"), "OUT=out"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("discard failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("discard removed a file through a link: %v", err)
	}
}
