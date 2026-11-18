// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package action tests the helper scripts of the composite action.
package action

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
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
	runToken         = "run-token-Zm9vYmFyYmF6"
	runID            = "6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90"
	driftRunID       = "0b7e5c1a-4d3f-5e92-8a16-9c2d4e6f8a10"
	sessionPath      = "/api/v1/session"
)

// documents are the ones the script sends, in the order it sends them.
var documents = []string{"ocsf-inventory", "run", "gate", "graph"}

// oidcToken is the fake identity token the fake OIDC endpoint returns. It
// is joined at run time so secret scanners don't read it as a real JWT.
var oidcToken = strings.Join([]string{"eyJhbGciOiJSUzI1NiJ9", "eyJhdWQiOiJ4In0", "c2lnbmF0dXJl"}, ".")

// fakeBinary stands in for infraharvest: it records its arguments and
// writes a document with a run_id to --out-file. As the real export does,
// the drift summary and the run of --command drift have the run_id of the
// drift check, the run document has the command and the request_id, and the
// OCSF inventory has its version and events.
const fakeBinary = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_ARGS_LOG"
printf 'key=%s\n' "$INFRAHARVEST_EXPORT_KEY" >> "$FAKE_ARGS_LOG"
command=import request=
for arg in "$@"; do
  case "$arg" in
    --out-file=*) file=${arg#--out-file=} ;;
    --schema=*) doc=${arg#--schema=} ;;
    --command=*) command=${arg#--command=} ;;
    --request-id=*) request=${arg#--request-id=} ;;
  esac
done
id=` + runID + `
if [[ "$doc" == drift-summary || "$command" == drift ]]; then id=` + driftRunID + `; fi
extra=
if [[ "$doc" == run ]]; then extra=",\"command\":\"$command\",\"request_id\":\"$request\""; fi
if [[ "$doc" == ocsf-inventory ]]; then extra=",\"ocsf_version\":\"1.9.0\",\"events\":[]"; fi
printf '{"schema":"%s/v1","run_id":"%s"%s}\n' "$doc" "$id" "$extra" > "$file"
`

// controlPlane is a fake control plane: it answers the OIDC token request,
// the session and the ingest endpoints, with the statuses a test queues for
// a path. The session wants the OIDC token in X-GitHub-OIDC-Token and the
// ingest endpoints the run token it gives; a request without it is a 401.
type controlPlane struct {
	*httptest.Server
	mu       sync.Mutex
	statuses map[string][]int // queued statuses per path; the last repeats
	calls    map[string]int
	headers  map[string]http.Header
	bodies   map[string]string // after gunzip
	reasons  map[string]string // the "error" of a refusal, per path
	plain    bool              // refuse with plain text, not JSON
	audience string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	cp := &controlPlane{statuses: map[string][]int{}, calls: map[string]int{}, headers: map[string]http.Header{}, bodies: map[string]string{}, reasons: map[string]string{}}
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
		if r.Header.Get("Content-Encoding") == "gzip" {
			plain, err := gunzip(body)
			if err != nil {
				plain = "gzip error: " + err.Error()
			}
			cp.bodies[r.URL.Path] = plain
		}
		queue := cp.statuses[r.URL.Path]
		status := http.StatusOK
		if len(queue) > 0 {
			status = queue[0]
			if len(queue) > 1 {
				cp.statuses[r.URL.Path] = queue[1:]
			}
		}
		if r.URL.Path == sessionPath {
			if r.Header.Get("X-GitHub-OIDC-Token") != oidcToken {
				status = http.StatusUnauthorized
			}
		} else if r.Header.Get("Authorization") != "Bearer "+runToken {
			status = http.StatusUnauthorized
		}
		w.WriteHeader(status)
		switch {
		case status >= 400:
			reason := cp.reasons[r.URL.Path]
			if reason == "" {
				reason = "nope: the server said no"
			}
			if cp.plain {
				fmt.Fprint(w, reason)
			} else {
				fmt.Fprintf(w, `{"error":%q}`, reason)
			}
		case r.URL.Path == sessionPath:
			fmt.Fprintf(w, `{"token":%q,"expires_at":"2026-11-18T10:15:00Z"}`, runToken)
		}
	}))
	t.Cleanup(cp.Close)
	return cp
}

func gunzip(b []byte) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	plain, err := io.ReadAll(zr)
	return string(plain), err
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

// layout is which reports the output directory has.
type layout struct{ report, drift bool }

// run runs the script in a directory with an output directory that has a
// report (an import's, unless noReport), with env added to the variables a
// step would have.
func run(t *testing.T, mode string, env map[string]string, noReport bool) result {
	t.Helper()
	return runIn(t, mode, env, layout{report: !noReport})
}

// runIn is run with the reports the output directory has.
func runIn(t *testing.T, mode string, env map[string]string, files layout) result {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s isn't installed", tool)
		}
	}
	dir := t.TempDir()
	reports := filepath.Join(dir, "out", "_reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, has := range map[string]bool{"report.json": files.report, "drift.json": files.drift} {
		if !has {
			continue
		}
		if err := os.WriteFile(filepath.Join(reports, name), []byte("{}"), 0o644); err != nil {
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
	cmd := exec.CommandContext(t.Context(), "bash", script, mode)
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

// upload runs the upload of an import with the OIDC endpoint of cp.
func upload(t *testing.T, cp *controlPlane, env map[string]string) result {
	t.Helper()
	return uploadIn(t, cp, env, layout{report: true})
}

// uploadIn is upload with the reports the output directory has.
func uploadIn(t *testing.T, cp *controlPlane, env map[string]string, files layout) result {
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
	return runIn(t, "upload", all, files)
}

// driftEnv is what the Control plane step has after a drift check and no
// import, with env added.
func driftEnv(env map[string]string) map[string]string {
	all := map[string]string{"IMPORT": "false", "IMPORT_OUTCOME": "skipped", "DRIFT": "report", "DRIFT_OUTCOME": "success"}
	for k, v := range env {
		all[k] = v
	}
	return all
}

// exports are the export commands the fake binary ran, in order.
func exports(res result) []string {
	var lines []string
	for _, line := range strings.Split(res.args, "\n") {
		if strings.HasPrefix(line, "export ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// parsed is the part of a document the tests read.
type parsed struct {
	Schema    string `json:"schema"`
	RunID     string `json:"run_id"`
	Command   string `json:"command"`
	RequestID string `json:"request_id"`
}

func (cp *controlPlane) document(t *testing.T, doc string) parsed {
	t.Helper()
	var p parsed
	if err := json.Unmarshal([]byte(cp.bodies["/api/v1/ingest/"+doc]), &p); err != nil {
		t.Fatalf("%s: body isn't JSON: %v\n%q", doc, err, cp.bodies["/api/v1/ingest/"+doc])
	}
	return p
}

func TestUploadSendsEachDocument(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	// The session is opened once, with the OIDC token and an empty body.
	if cp.count(sessionPath) != 1 {
		t.Errorf("session: %d requests, want 1", cp.count(sessionPath))
	}
	sh := cp.headers[sessionPath]
	if got := sh.Get("X-GitHub-OIDC-Token"); got != oidcToken {
		t.Errorf("session: X-GitHub-OIDC-Token is %q", got)
	}
	if got := sh.Get("Authorization"); got != "" {
		t.Errorf("session: Authorization is %q, want none", got)
	}
	if cp.bodies[sessionPath] != "" {
		t.Errorf("session: body is %q, want empty", cp.bodies[sessionPath])
	}
	for _, doc := range documents {
		path := "/api/v1/ingest/" + doc
		if cp.count(path) != 1 {
			t.Errorf("%s: %d requests, want 1", path, cp.count(path))
		}
		h := cp.headers[path]
		// The run token is the bearer token, never the OIDC token.
		if got := h.Get("Authorization"); got != "Bearer "+runToken {
			t.Errorf("%s: Authorization is %q", path, got)
		}
		if got := h.Get("X-GitHub-OIDC-Token"); got != "" {
			t.Errorf("%s: the OIDC token is sent: %q", path, got)
		}
		if got := h.Get("Idempotency-Key"); got != runID+":"+doc {
			t.Errorf("%s: Idempotency-Key is %q", path, got)
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type is %q", path, got)
		}
		if got := h.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s: Content-Encoding is %q", path, got)
		}
		// The body, decompressed, is the document.
		var parsed struct {
			Schema string `json:"schema"`
			RunID  string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(cp.bodies[path]), &parsed); err != nil {
			t.Errorf("%s: body isn't JSON after gunzip: %v\n%q", path, err, cp.bodies[path])
		} else if parsed.Schema != doc+"/v1" || parsed.RunID != runID {
			t.Errorf("%s: body is %q", path, cp.bodies[path])
		}
	}
	// The documents are exported in the order they are sent, graph last.
	var exported []string
	for _, line := range strings.Split(res.args, "\n") {
		if strings.HasPrefix(line, "export ") {
			exported = append(exported, line)
		}
	}
	if len(exported) != len(documents) || !strings.Contains(exported[len(exported)-1], "--schema=graph") {
		t.Errorf("exports: %q", exported)
	}
	if cp.audience != "infraharvest-api" {
		t.Errorf("token audience is %q", cp.audience)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
	// Both tokens are masked, and printed nowhere else.
	for _, token := range []string{oidcToken, runToken} {
		if !strings.Contains(res.output, "::add-mask::"+token+"\n") {
			t.Errorf("the token isn't masked:\n%s", res.output)
		}
		rest := strings.ReplaceAll(res.output, "::add-mask::"+token, "")
		if strings.Contains(rest, token) {
			t.Errorf("a token is printed:\n%s", res.output)
		}
	}
	if strings.Contains(res.output, oidcRequestToken) {
		t.Errorf("the request token is printed:\n%s", res.output)
	}
}

func TestUploadUsesTheAudience(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"AUDIENCE": "https://cp.example.com/api"})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if cp.audience != "https://cp.example.com/api" {
		t.Errorf("token audience is %q", cp.audience)
	}
}

func TestUploadOpensTheSessionBeforeExporting(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(sessionPath, 402)
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if res.args != "" {
		t.Errorf("exported before the session was open: %q", res.args)
	}
}

func TestUploadSendsTheGraph(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123", "DROP_TAGS": "true"})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if cp.count("/api/v1/ingest/graph") != 1 || !strings.Contains(cp.bodies["/api/v1/ingest/graph"], `"schema":"graph/v1"`) {
		t.Errorf("graph: %d requests, body %q", cp.count("/api/v1/ingest/graph"), cp.bodies["/api/v1/ingest/graph"])
	}
	// The graph has IDs to hash, but no tags to drop.
	var graph string
	for _, line := range strings.Split(res.args, "\n") {
		if strings.Contains(line, "--schema=graph") {
			graph = line
		}
	}
	if !strings.Contains(graph, "--hash-ids") || strings.Contains(graph, "--drop") {
		t.Errorf("graph export ran as %q", graph)
	}
}

func TestUploadFailsWhenTheGraphFails(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/graph", 400)
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if cp.count("/api/v1/ingest/gate") != 1 || cp.count("/api/v1/ingest/graph") != 1 {
		t.Errorf("gate %d, graph %d requests", cp.count("/api/v1/ingest/gate"), cp.count("/api/v1/ingest/graph"))
	}
	if strings.Contains(res.outputs, "control-plane-run-id") {
		t.Errorf("run id set without the graph: %q", res.outputs)
	}
}

func TestUploadPlanRefusalIsNotRetried(t *testing.T) {
	for _, path := range []string{sessionPath, "/api/v1/ingest/ocsf-inventory", "/api/v1/ingest/graph"} {
		t.Run(path, func(t *testing.T) {
			cp := newControlPlane(t)
			cp.queue(path, 402)
			cp.reasons[path] = "cloud account limit reached (3 of 3)"
			res := upload(t, cp, nil)
			if res.err == nil {
				t.Fatalf("upload succeeded:\n%s", res.output)
			}
			if got := cp.count(path); got != 1 {
				t.Errorf("%d requests, want 1", got)
			}
			for _, want := range []string{"::error::", "HTTP 402", "plan refused", "cloud account limit reached (3 of 3)", "not a problem with the token"} {
				if !strings.Contains(res.output, want) {
					t.Errorf("output lacks %q:\n%s", want, res.output)
				}
			}
			if strings.Contains(res.output, `{"error"`) {
				t.Errorf("the JSON body is printed as it came:\n%s", res.output)
			}
			if strings.Contains(res.outputs, "control-plane-run-id") {
				t.Errorf("outputs: %q", res.outputs)
			}
		})
	}
}

func TestUploadRefusedSession(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			cp := newControlPlane(t)
			cp.queue(sessionPath, status)
			cp.reasons[sessionPath] = "audience not accepted"
			res := upload(t, cp, nil)
			if res.err == nil {
				t.Fatalf("upload succeeded:\n%s", res.output)
			}
			if got := cp.count(sessionPath); got != 1 {
				t.Errorf("session: %d requests, want 1", got)
			}
			for _, want := range []string{fmt.Sprintf("HTTP %d", status), "refused the token", "audience not accepted"} {
				if !strings.Contains(res.output, want) {
					t.Errorf("output lacks %q:\n%s", want, res.output)
				}
			}
			if cp.count("/api/v1/ingest/ocsf-inventory") != 0 {
				t.Error("uploaded without a session")
			}
		})
	}
}

func TestUploadSessionRetriesServerErrors(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(sessionPath, 503, 200)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := cp.count(sessionPath); got != 2 {
		t.Errorf("session: %d requests, want 2", got)
	}
}

func TestUploadSessionWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oidc":
			fmt.Fprintf(w, `{"value":%q}`, oidcToken)
		default:
			fmt.Fprint(w, `{"expires_at":"2026-11-18T10:15:00Z"}`)
		}
	}))
	t.Cleanup(srv.Close)
	res := run(t, "upload", map[string]string{
		"URL": srv.URL, "ACTIONS_ID_TOKEN_REQUEST_URL": srv.URL + "/oidc", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": oidcRequestToken,
	}, false)
	if res.err == nil || !strings.Contains(res.output, "no usable token") {
		t.Errorf("want a failure about the session's token: %v\n%s", res.err, res.output)
	}
}

func TestUploadRefusalWithoutJSONShowsTheBody(t *testing.T) {
	cp := newControlPlane(t)
	cp.plain = true
	cp.queue("/api/v1/ingest/run", 422)
	cp.reasons["/api/v1/ingest/run"] = "schema mismatch at /counts"
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if !strings.Contains(res.output, "HTTP 422") || !strings.Contains(res.output, "schema mismatch at /counts") {
		t.Errorf("output lacks the body:\n%s", res.output)
	}
	if got := cp.count("/api/v1/ingest/run"); got != 1 {
		t.Errorf("run: %d requests, want 1", got)
	}
}

func TestUploadRetriesServerErrorsAndRateLimits(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue("/api/v1/ingest/ocsf-inventory", 503, 500, 200)
	cp.queue("/api/v1/ingest/run", 429, 200)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := cp.count("/api/v1/ingest/ocsf-inventory"); got != 3 {
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
	cp.queue("/api/v1/ingest/ocsf-inventory", 502)
	res := upload(t, cp, nil)
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if got := cp.count("/api/v1/ingest/ocsf-inventory"); got != 4 {
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
	cp.queue("/api/v1/ingest/ocsf-inventory", 401)
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
	if cp.count("/api/v1/ingest/ocsf-inventory") != 0 {
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
	if want := "export out --schema=ocsf-inventory --out-file="; !strings.HasPrefix(lines[0], want) ||
		!strings.Contains(lines[0], "--drop-tags --drop-tag=owner-email --drop-tag=cost-center --hash-ids") {
		t.Errorf("OCSF inventory export ran as %q", lines[0])
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
	if cp.count("/api/v1/ingest/ocsf-inventory") != 0 {
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
		{"audience", map[string]string{"URL": "https://cp.example.com", "AUDIENCE": "https://cp.example.com/api"}, ""},
		{"bad audience", map[string]string{"URL": "https://cp.example.com", "AUDIENCE": "a&b=c"}, "control-plane-audience"},
		{"drift only", map[string]string{"URL": "https://cp.example.com", "IMPORT": "false", "DRIFT": "report"}, ""},
		{"request id", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": "01HZX-7:a.b_c"}, ""},
		{"longest request id", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": strings.Repeat("a", 128)}, ""},
		{"request id too long", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": strings.Repeat("a", 129)}, "control-plane-request-id"},
		{"request id with a space", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": "a b"}, "control-plane-request-id"},
		{"request id with a newline", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": "a\nb=c"}, "control-plane-request-id"},
		{"request id with a slash", map[string]string{"URL": "https://cp.example.com", "REQUEST_ID": "a/b"}, "control-plane-request-id"},
		{"bad drop addresses", map[string]string{"URL": "https://cp.example.com", "DROP_ADDRESSES": "yes"}, "control-plane-drop-addresses must be true or false"},
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

func TestUploadPassesTheImportTimes(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{
		"STARTED_AT":  "2026-11-03T09:30:00Z",
		"FINISHED_AT": "2026-11-03T09:37:00Z",
	})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	started, finished := "--started-at=2026-11-03T09:30:00Z", "--finished-at=2026-11-03T09:37:00Z"
	var exports []string
	for _, line := range strings.Split(res.args, "\n") {
		if strings.HasPrefix(line, "export ") {
			exports = append(exports, line)
		}
	}
	if len(exports) != 4 {
		t.Fatalf("exports: %q", exports)
	}
	// The run has both. The OCSF inventory has the finish, which is the time
	// of its events, and not the start; the gate and the graph have no time.
	for i, want := range [][2]bool{{false, true}, {true, true}, {false, false}, {false, false}} {
		if got := strings.Contains(exports[i], started); got != want[0] {
			t.Errorf("export %d ran as %q, start wanted: %v", i, exports[i], want[0])
		}
		if got := strings.Contains(exports[i], finished); got != want[1] {
			t.Errorf("export %d ran as %q, finish wanted: %v", i, exports[i], want[1])
		}
	}
}

func TestUploadWithoutImportTimes(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"STARTED_AT": "", "FINISHED_AT": ""})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if strings.Contains(res.args, "--started-at") || strings.Contains(res.args, "--finished-at") {
		t.Errorf("exports ran as %q", res.args)
	}
}

func TestUploadOfADriftCheckSendsTheRunAndTheSummary(t *testing.T) {
	cp := newControlPlane(t)
	// The repository has an import's report too, which isn't this run's.
	res := uploadIn(t, cp, driftEnv(map[string]string{"STARTED_AT": "", "DROP_TAGS": "true", "DRIFT_STARTED_AT": "2026-11-03T10:00:00Z", "DRIFT_FINISHED_AT": "2026-11-03T10:02:00Z"}), layout{report: true, drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	for _, doc := range []string{"ocsf-inventory", "gate", "graph"} {
		if n := cp.count("/api/v1/ingest/" + doc); n != 0 {
			t.Errorf("%s: %d requests, want none", doc, n)
		}
	}
	got := exports(res)
	if len(got) != 2 || !strings.Contains(got[0], "--schema=run") || !strings.Contains(got[0], "--command=drift") ||
		!strings.Contains(got[1], "--schema=drift-summary") {
		t.Fatalf("exports: %q", got)
	}
	// The drift check's own times go to its run document, not to the
	// summary, and the tag flags are the inventory's.
	if !strings.Contains(got[0], "--started-at=2026-11-03T10:00:00Z --finished-at=2026-11-03T10:02:00Z") ||
		strings.Contains(got[1], "--started-at") || strings.Contains(got[1], "--finished-at") || strings.Contains(res.args, "--drop-tags") {
		t.Errorf("exports ran as %q", res.args)
	}
	for _, doc := range []string{"run", "drift-summary"} {
		path := "/api/v1/ingest/" + doc
		if cp.count(path) != 1 {
			t.Errorf("%s: %d requests, want 1", path, cp.count(path))
		}
		if got := cp.headers[path].Get("Idempotency-Key"); got != driftRunID+":"+doc {
			t.Errorf("%s: Idempotency-Key is %q", path, got)
		}
	}
	if cmd := cp.document(t, "run").Command; cmd != "drift" {
		t.Errorf("run document's command is %q", cmd)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+driftRunID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadOfAnImportAndADriftCheckSendsFive(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, map[string]string{"DRIFT": "accept", "DRIFT_OUTCOME": "success", "STARTED_AT": "2026-11-03T09:30:00Z", "FINISHED_AT": "2026-11-03T09:37:00Z"},
		layout{report: true, drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	want := []string{"ocsf-inventory", "run", "gate", "graph", "drift-summary"}
	got := exports(res)
	if len(got) != len(want) {
		t.Fatalf("exports: %q", got)
	}
	for i, doc := range want {
		if !strings.Contains(got[i], "--schema="+doc+" ") {
			t.Errorf("export %d ran as %q, want %s", i, got[i], doc)
		}
		if cp.count("/api/v1/ingest/"+doc) != 1 {
			t.Errorf("%s: %d requests, want 1", doc, cp.count("/api/v1/ingest/"+doc))
		}
	}
	// The run document is the import's, with its times; the drift summary
	// has the drift check's run_id, and none.
	if strings.Contains(got[1], "--command") || !strings.Contains(got[1], "--started-at=2026-11-03T09:30:00Z") {
		t.Errorf("run export ran as %q", got[1])
	}
	if strings.Contains(got[4], "--started-at") || strings.Contains(got[4], "--finished-at") {
		t.Errorf("drift summary export ran as %q", got[4])
	}
	if cmd := cp.document(t, "run").Command; cmd != "import" {
		t.Errorf("run document's command is %q", cmd)
	}
	if got := cp.headers["/api/v1/ingest/drift-summary"].Get("Idempotency-Key"); got != driftRunID+":drift-summary" {
		t.Errorf("drift-summary: Idempotency-Key is %q", got)
	}
	if got := cp.headers["/api/v1/ingest/run"].Get("Idempotency-Key"); got != runID+":run" {
		t.Errorf("run: Idempotency-Key is %q", got)
	}
	// The run id is the import's, set once all five were accepted.
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadOfAnImportDoesNotSendADriftSummary(t *testing.T) {
	cp := newControlPlane(t)
	// drift.json is there from an earlier run, and the Drift step didn't run.
	res := uploadIn(t, cp, map[string]string{"DRIFT": "off", "DRIFT_OUTCOME": "skipped"}, layout{report: true, drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if n := len(exports(res)); n != 4 || cp.count("/api/v1/ingest/drift-summary") != 0 {
		t.Errorf("%d exports, %d drift summaries", n, cp.count("/api/v1/ingest/drift-summary"))
	}
}

func TestUploadOfADriftCheckThatFailedStillSends(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, driftEnv(map[string]string{"DRIFT": "accept", "DRIFT_OUTCOME": "failure"}), layout{drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if cp.count("/api/v1/ingest/run") != 1 || cp.count("/api/v1/ingest/drift-summary") != 1 {
		t.Errorf("run %d, drift summary %d requests", cp.count("/api/v1/ingest/run"), cp.count("/api/v1/ingest/drift-summary"))
	}
}

func TestUploadOfADriftCheckWithoutDriftJSON(t *testing.T) {
	cp := newControlPlane(t)
	// An import's report is there, but the run is the drift check's.
	res := uploadIn(t, cp, driftEnv(nil), layout{report: true})
	if res.err != nil || !strings.Contains(res.output, "::warning::control plane: the drift check left no drift report") {
		t.Errorf("want a warning and no failure: %v\n%s", res.err, res.output)
	}
	if cp.count(sessionPath) != 0 || res.args != "" || strings.Contains(res.outputs, "control-plane-run-id") {
		t.Errorf("sent something: %d sessions, exports %q, outputs %q", cp.count(sessionPath), res.args, res.outputs)
	}
}

func TestUploadOfADriftSummaryPassesTheRedactionFlags(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		want []string
		not  []string
	}{
		{"hashed", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123"}, []string{"--hash-ids"}, []string{"--drop-addresses"}},
		{"dropped", map[string]string{"DROP_ADDRESSES": "true"}, []string{"--drop-addresses"}, []string{"--hash-ids"}},
		{"both", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123", "DROP_ADDRESSES": "true"}, []string{"--hash-ids", "--drop-addresses"}, nil},
		{"plain", map[string]string{"DROP_ADDRESSES": "false"}, nil, []string{"--hash-ids", "--drop-addresses"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			res := uploadIn(t, cp, driftEnv(tt.env), layout{drift: true})
			if res.err != nil {
				t.Fatalf("upload failed: %v\n%s", res.err, res.output)
			}
			got := exports(res)
			if len(got) != 2 {
				t.Fatalf("exports: %q", got)
			}
			for _, flag := range tt.want {
				if !strings.Contains(got[1], flag) {
					t.Errorf("drift summary export ran as %q, want %s", got[1], flag)
				}
			}
			for _, flag := range tt.not {
				if strings.Contains(got[1], flag) {
					t.Errorf("drift summary export ran as %q, not %s", got[1], flag)
				}
			}
			// The run document has no address to hash or drop.
			if strings.Contains(got[0], "--hash-ids") || strings.Contains(got[0], "--drop-addresses") {
				t.Errorf("run export ran as %q", got[0])
			}
		})
	}
}

func TestUploadPassesTheRequestID(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		files layout
	}{
		{"import", map[string]string{"REQUEST_ID": "01HZX-7:a.b_c"}, layout{report: true}},
		{"drift", driftEnv(map[string]string{"REQUEST_ID": "01HZX-7:a.b_c"}), layout{drift: true}},
		{"both", map[string]string{"REQUEST_ID": "01HZX-7:a.b_c", "DRIFT": "report", "DRIFT_OUTCOME": "success"}, layout{report: true, drift: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			res := uploadIn(t, cp, tt.env, tt.files)
			if res.err != nil {
				t.Fatalf("upload failed: %v\n%s", res.err, res.output)
			}
			// It reaches the run document, and only that one.
			if got := cp.document(t, "run").RequestID; got != "01HZX-7:a.b_c" {
				t.Errorf("run document's request_id is %q", got)
			}
			for _, line := range exports(res) {
				if has := strings.Contains(line, "--request-id=01HZX-7:a.b_c"); has != strings.Contains(line, "--schema=run ") {
					t.Errorf("export ran as %q", line)
				}
			}
		})
	}
}

func TestUploadWithoutARequestID(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if strings.Contains(res.args, "--request-id") {
		t.Errorf("exports ran as %q", res.args)
	}
}

// The OCSF inventory is what the Action sends of the inventory: it takes the
// redaction flags, --drop-addresses and the import's finish, and the
// repository and the commit come from the GITHUB_* variables export reads,
// not from flags. inventory/v1 is exported and sent no more.
func TestUploadSendsTheOCSFInventory(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		want []string
		not  []string
	}{
		{"plain", nil, nil, []string{"--hash-ids", "--drop-addresses", "--drop-tags"}},
		{"hashed", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123"}, []string{"--hash-ids"}, []string{"--drop-addresses"}},
		{"dropped", map[string]string{"DROP_ADDRESSES": "true"}, []string{"--drop-addresses"}, []string{"--hash-ids"}},
		{"both", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123", "DROP_ADDRESSES": "true", "DROP_TAGS": "true"}, []string{"--hash-ids", "--drop-addresses", "--drop-tags"}, nil},
		{"not dropped", map[string]string{"DROP_ADDRESSES": "false", "DROP_TAG": "owner-email"}, []string{"--drop-tag=owner-email"}, []string{"--drop-addresses"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			env := map[string]string{"FINISHED_AT": "2026-11-03T09:37:00Z"}
			for k, v := range tt.env {
				env[k] = v
			}
			res := upload(t, cp, env)
			if res.err != nil {
				t.Fatalf("upload failed: %v\n%s", res.err, res.output)
			}
			got := exports(res)
			if len(got) != 4 || !strings.Contains(got[0], "--schema=ocsf-inventory ") {
				t.Fatalf("exports: %q", got)
			}
			for _, flag := range tt.want {
				if !strings.Contains(got[0], flag) {
					t.Errorf("OCSF inventory export ran as %q, want %s", got[0], flag)
				}
			}
			for _, flag := range append([]string{"--repo-url", "--commit-sha", "--started-at"}, tt.not...) {
				if strings.Contains(got[0], flag) {
					t.Errorf("OCSF inventory export ran as %q, not %s", got[0], flag)
				}
			}
			if !strings.Contains(got[0], "--finished-at=2026-11-03T09:37:00Z") {
				t.Errorf("OCSF inventory export ran as %q, want the finish", got[0])
			}
			// The route, the key and the body are the OCSF inventory's; the
			// inventory document is neither exported nor sent.
			path := "/api/v1/ingest/ocsf-inventory"
			if n := cp.count(path); n != 1 {
				t.Errorf("%s: %d requests, want 1", path, n)
			}
			if got := cp.headers[path].Get("Idempotency-Key"); got != runID+":ocsf-inventory" {
				t.Errorf("Idempotency-Key is %q", got)
			}
			if body := cp.bodies[path]; !strings.Contains(body, `"schema":"ocsf-inventory/v1"`) || !strings.Contains(body, `"ocsf_version":"1.9.0"`) {
				t.Errorf("body is %q", body)
			}
			if cp.count("/api/v1/ingest/inventory") != 0 || strings.Contains(res.args, "--schema=inventory") {
				t.Errorf("inventory/v1 was exported or sent: %d requests, %q", cp.count("/api/v1/ingest/inventory"), res.args)
			}
		})
	}
}
