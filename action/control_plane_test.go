// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package action tests the helper scripts of the composite action.
package action

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	oidcRequestToken = "request-token-1234"
	oidcToken        = "eyJhbGciOiJSUzI1NiJ9.eyJhdWQiOiJ4In0.c2lnbmF0dXJl"
	runID            = "6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90"
)

// fakeBinary stands in for infraharvest: it records its arguments and
// writes a document with a run_id to --out-file.
const fakeBinary = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_ARGS_LOG"
printf 'key=%s\n' "$INFRAHARVEST_EXPORT_KEY" >> "$FAKE_ARGS_LOG"
for arg in "$@"; do
  case "$arg" in --out-file=*) file=${arg#--out-file=} ;; --schema=*) doc=${arg#--schema=} ;; esac
done
printf '{"schema":"%s/v1","run_id":"` + runID + `"}\n' "$doc" > "$file"
`

// controlPlane is a fake control plane: it answers the OIDC token request
// and the ingest endpoints, with the statuses a test queues for a path.
type controlPlane struct {
	*httptest.Server
	mu       sync.Mutex
	statuses map[string][]int // queued statuses per path; the last repeats
	calls    map[string]int
	headers  map[string]http.Header
	bodies   map[string]string
	audience string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	cp := &controlPlane{statuses: map[string][]int{}, calls: map[string]int{}, headers: map[string]http.Header{}, bodies: map[string]string{}}
	cp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		if r.URL.Path == "/oidc" {
			cp.audience = r.URL.Query().Get("audience")
			if r.Header.Get("Authorization") != "Bearer "+oidcRequestToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"value":%q}`, oidcToken)
			return
		}
		body, _ := io.ReadAll(r.Body)
		cp.calls[r.URL.Path]++
		cp.headers[r.URL.Path] = r.Header.Clone()
		cp.bodies[r.URL.Path] = string(body)
		queue := cp.statuses[r.URL.Path]
		status := http.StatusOK
		if len(queue) > 0 {
			status = queue[0]
			if len(queue) > 1 {
				cp.statuses[r.URL.Path] = queue[1:]
			}
		}
		w.WriteHeader(status)
		if status >= 400 {
			fmt.Fprint(w, "nope: the server said no")
		}
	}))
	t.Cleanup(cp.Close)
	return cp
}

func (cp *controlPlane) queue(path string, statuses ...int) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.statuses[path] = statuses
}

func (cp *controlPlane) count(path string) int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.calls[path]
}

type result struct {
	output  string // stdout and stderr
	outputs string // $GITHUB_OUTPUT
	args    string // what the fake binary was run with
	err     error
}

// run runs the script in a directory with an output directory that has a
// report, with env added to the variables a step would have.
func run(t *testing.T, mode string, env map[string]string, noReport bool) result {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s isn't installed", tool)
		}
	}
	dir := t.TempDir()
	if !noReport {
		reports := filepath.Join(dir, "out", "_reports")
		if err := os.MkdirAll(reports, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reports, "report.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin", "infraharvest")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(dir, "github-output")
	argsLog := filepath.Join(dir, "args.log")
	script, err := filepath.Abs("control-plane.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, mode)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"RUNNER_TEMP=" + dir,
		"GITHUB_OUTPUT=" + githubOutput,
		"BINARY=" + bin,
		"OUT=out",
		"IMPORT=true",
		"IMPORT_OUTCOME=success",
		"FAKE_ARGS_LOG=" + argsLog,
		"INFRAHARVEST_UPLOAD_RETRY_DELAY=0",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	outputs, _ := os.ReadFile(githubOutput)
	args, _ := os.ReadFile(argsLog)
	return result{output: out.String(), outputs: string(outputs), args: string(args), err: runErr}
}

// upload runs the upload with the OIDC endpoint of cp.
func upload(t *testing.T, cp *controlPlane, env map[string]string) result {
	t.Helper()
	all := map[string]string{
		"URL":                            cp.URL + "/",
		"OPTIONAL":                       "false",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   cp.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": oidcRequestToken,
	}
	for k, v := range env {
		all[k] = v
	}
	return run(t, "upload", all, false)
}

func TestUploadSendsEachDocument(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	for _, doc := range []string{"inventory", "run", "gate"} {
		path := "/api/v1/ingest/" + doc
		if cp.count(path) != 1 {
			t.Errorf("%s: %d requests, want 1", path, cp.count(path))
		}
		h := cp.headers[path]
		if got := h.Get("Authorization"); got != "Bearer "+oidcToken {
			t.Errorf("%s: Authorization is %q", path, got)
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type is %q", path, got)
		}
		if !strings.Contains(cp.bodies[path], `"schema":"`+doc+`/v1"`) {
			t.Errorf("%s: body is %q", path, cp.bodies[path])
		}
	}
	if cp.audience != "infraharvest-api" {
		t.Errorf("token audience is %q", cp.audience)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
	// The token is masked, and printed nowhere else.
	if !strings.Contains(res.output, "::add-mask::"+oidcToken+"\n") {
		t.Errorf("the token isn't masked:\n%s", res.output)
	}
	if rest := strings.ReplaceAll(res.output, "::add-mask::"+oidcToken, ""); strings.Contains(rest, oidcToken) || strings.Contains(rest, oidcRequestToken) {
		t.Errorf("a token is printed:\n%s", res.output)
	}
}

func TestUploadRetriesServerErrorsAndRateLimits(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/inventory", 503, 500, 200)
	cp.queue("/api/v1/ingest/run", 429, 200)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := cp.count("/api/v1/ingest/inventory"); got != 3 {
		t.Errorf("inventory: %d requests, want 3", got)
	}
	if got := cp.count("/api/v1/ingest/run"); got != 2 {
		t.Errorf("run: %d requests, want 2", got)
	}
	if got := cp.count("/api/v1/ingest/gate"); got != 1 {
		t.Errorf("gate: %d requests, want 1", got)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID) {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadGivesUpOnPersistentServerErrors(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/inventory", 502)
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if got := cp.count("/api/v1/ingest/inventory"); got != 4 {
		t.Errorf("inventory: %d requests, want 4", got)
	}
	if cp.count("/api/v1/ingest/run") != 0 || strings.Contains(res.outputs, "control-plane-run-id") {
		t.Errorf("went on after a failure:\n%s", res.output)
	}
}

func TestUploadFailsOnClientErrorWithoutRetry(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/run", 403)
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if got := cp.count("/api/v1/ingest/run"); got != 1 {
		t.Errorf("run: %d requests, want 1", got)
	}
	for _, want := range []string{"::error::", "HTTP 403", "the server said no"} {
		if !strings.Contains(res.output, want) {
			t.Errorf("output lacks %q:\n%s", want, res.output)
		}
	}
	if strings.Contains(res.outputs, "control-plane-run-id") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadOptionalOnlyWarns(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/inventory", 401)
	res := upload(t, cp, map[string]string{"OPTIONAL": "true"})
	if res.err != nil {
		t.Fatalf("upload failed although optional: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.output, "::warning::") || strings.Contains(res.output, "::error::") {
		t.Errorf("want a warning only:\n%s", res.output)
	}
}

func TestUploadWithoutOIDCPermission(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": ""})
	if res.err == nil || !strings.Contains(res.output, "id-token: write") {
		t.Errorf("want a failure that names id-token: write: %v\n%s", res.err, res.output)
	}
	if cp.count("/api/v1/ingest/inventory") != 0 {
		t.Error("sent without a token")
	}
}

func TestUploadRejectedOIDCRequest(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "wrong"})
	if res.err == nil || !strings.Contains(res.output, "couldn't request a GitHub Actions OIDC token") {
		t.Errorf("want a failure about the token: %v\n%s", res.err, res.output)
	}
}

func TestUploadPassesTheRedactionFlags(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{
		"DROP_TAGS": "true",
		"DROP_TAG":  "owner-email,\ncost-center",
		"HASH_IDS":  "true",
		"HASH_KEY":  "0123456789abcdef0123",
	})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	lines := strings.Split(res.args, "\n")
	if want := "export out --schema=inventory --out-file="; !strings.HasPrefix(lines[0], want) ||
		!strings.Contains(lines[0], "--drop-tags --drop-tag=owner-email --drop-tag=cost-center --hash-ids") {
		t.Errorf("inventory export ran as %q", lines[0])
	}
	if lines[1] != "key=0123456789abcdef0123" {
		t.Errorf("key: %q", lines[1])
	}
	// The run and the gate document have nothing to redact.
	if strings.Contains(lines[2], "--drop") || strings.Contains(lines[2], "--hash") {
		t.Errorf("run export ran as %q", lines[2])
	}
}

func TestUploadSkipsAFailedImportWithoutReport(t *testing.T) {
	cp := newControlPlane(t)
	res := run(t, "upload", map[string]string{
		"URL": cp.URL, "IMPORT_OUTCOME": "failure",
		"ACTIONS_ID_TOKEN_REQUEST_URL": cp.URL + "/oidc", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": oidcRequestToken,
	}, true)
	if res.err != nil || !strings.Contains(res.output, "::warning::") {
		t.Errorf("want a warning and no failure: %v\n%s", res.err, res.output)
	}
	if cp.count("/api/v1/ingest/inventory") != 0 {
		t.Error("sent without a report")
	}
	// After an import that succeeded, a missing report is an error.
	res = run(t, "upload", map[string]string{"URL": cp.URL}, true)
	if res.err == nil {
		t.Errorf("want a failure:\n%s", res.output)
	}
}

func TestCheckInputs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // empty: passes
	}{
		{"off", map[string]string{"URL": ""}, ""},
		{"https", map[string]string{"URL": "https://cp.example.com/base/"}, ""},
		{"localhost", map[string]string{"URL": "http://localhost:8080"}, ""},
		{"loopback", map[string]string{"URL": "http://127.0.0.1:8080/x"}, ""},
		{"http", map[string]string{"URL": "http://cp.example.com"}, "https://"},
		{"lookalike", map[string]string{"URL": "http://localhost.evil.example"}, "https://"},
		{"user", map[string]string{"URL": "https://user:pw@cp.example.com"}, "https://"},
		{"query", map[string]string{"URL": "https://cp.example.com/?a=b"}, "https://"},
		{"not a url", map[string]string{"URL": "cp.example.com"}, "https://"},
		{"no import", map[string]string{"URL": "https://cp.example.com", "IMPORT": "false"}, "needs import"},
		{"bad boolean", map[string]string{"URL": "https://cp.example.com", "OPTIONAL": "yes"}, "must be true or false"},
		{"hash without key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true"}, "needs a key"},
		{"hash with env key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "INFRAHARVEST_EXPORT_KEY": "k"}, ""},
		{"key without hash", map[string]string{"URL": "https://cp.example.com", "HASH_KEY": "k"}, "takes effect"},
		{"no oidc", map[string]string{"URL": "https://cp.example.com", "ACTIONS_ID_TOKEN_REQUEST_URL": ""}, "id-token: write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"ACTIONS_ID_TOKEN_REQUEST_URL": "https://token.example.com/", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "t",
			}
			for k, v := range tt.env {
				env[k] = v
			}
			res := run(t, "check", env, false)
			if tt.want == "" {
				if res.err != nil {
					t.Errorf("failed: %v\n%s", res.err, res.output)
				}
				return
			}
			if res.err == nil || !strings.Contains(res.output, tt.want) {
				t.Errorf("want a failure mentioning %q: %v\n%s", tt.want, res.err, res.output)
			}
		})
	}
}
