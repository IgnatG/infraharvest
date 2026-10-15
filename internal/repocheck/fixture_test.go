// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repositoryFixture is a scratch Git repository that the repository's
// scripts and configuration files are checked against, isolated from the
// user's and the system's Git configuration.
type repositoryFixture struct {
	t   *testing.T
	dir string
	env []string
}

func newRepositoryFixture(t *testing.T) *repositoryFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("repository shell checks require a Unix environment")
	}
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("repository checks require %s: %v", tool, err)
		}
	}
	// Include a space in the root to exercise the scripts' directory quoting.
	dir := filepath.Join(t.TempDir(), "fixture repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") && !strings.HasPrefix(value, "LC_ALL=") {
			env = append(env, value)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir), "LC_ALL=C")
	repo := &repositoryFixture{t: t, dir: dir, env: env}
	repo.git(0, "init", "--quiet")
	return repo
}

// install tracks a copy of the repository's own file at the same path.
func (repo *repositoryFixture) install(path string) {
	repo.t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	if err != nil {
		repo.t.Fatal(err)
	}
	repo.track(path, string(content))
}

func (repo *repositoryFixture) write(path, content string) {
	repo.t.Helper()
	full := filepath.Join(repo.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		repo.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		repo.t.Fatal(err)
	}
}

func (repo *repositoryFixture) track(path, content string) {
	repo.t.Helper()
	repo.write(path, content)
	repo.git(0, "add", "--", path)
}

// git runs git in the fixture repository and checks its exit status.
func (repo *repositoryFixture) git(wantStatus int, args ...string) {
	repo.t.Helper()
	repo.commandAt(repo.dir, wantStatus, "git", args...)
}

func (repo *repositoryFixture) commandAt(dir string, wantStatus int, name string, args ...string) string {
	repo.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = repo.env
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		repo.t.Fatalf("%s %v timed out: %s", name, args, output)
	}
	status := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			repo.t.Fatalf("%s %v: %v", name, args, err)
		}
		status = exitErr.ExitCode()
	}
	if status != wantStatus {
		repo.t.Fatalf("%s %v exited %d, want %d:\n%s", name, args, status, wantStatus, output)
	}
	return string(output)
}

// runScript runs the fixture's copy of script with args, checks its exit
// status and that its output contains wantOutput, and returns the output.
// It starts outside the fixture: the script must find its own repository.
func (repo *repositoryFixture) runScript(script string, wantStatus int, wantOutput string, args ...string) string {
	repo.t.Helper()
	path := filepath.Join(repo.dir, filepath.FromSlash(script))
	output := repo.commandAt(filepath.Dir(repo.dir), wantStatus, "bash", append([]string{path}, args...)...)
	if !strings.Contains(output, wantOutput) {
		repo.t.Fatalf("%s %v: output must contain %q:\n%s", script, args, wantOutput, output)
	}
	return output
}
