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
	"net"
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
	storagePrefix    = "/storage/"
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
# FAKE_PAD_RANDOM bytes of text that gzip can't shrink, and FAKE_PAD_ZERO of
# text it shrinks to nothing, make the inventory as large as a test needs.
if [[ "$doc" == ocsf-inventory && -n "${FAKE_PAD_RANDOM-}" ]]; then extra+=",\"pad\":\"$(head -c "$FAKE_PAD_RANDOM" /dev/urandom | od -An -tx1 | tr -d ' \n')\""; fi
if [[ "$doc" == ocsf-inventory && -n "${FAKE_PAD_ZERO-}" ]]; then extra+=",\"pad\":\"$(head -c "$FAKE_PAD_ZERO" /dev/zero | tr '\0' 'a')\""; fi
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
	sizes    map[string]int    // as received, before gunzip
	reasons  map[string]string // the "error" of a refusal, per path
	// retryAfterMs, when positive, goes in the body of a 429 as retryAfterMs,
	// and retryAfterHeader, when set, in its Retry-After header.
	retryAfterMs     int
	retryAfterHeader string
	rateLimitBody    string // when set, the whole body of a 429

	answers map[string]string // the JSON body of a success, per path, when a test sets one

	sessionLimits string // when set, the "limits" of the session answer, as JSON
	sessionExtra  string // when set, more members of the session answer, such as `"entitlements":{}`
	plain         bool   // refuse with plain text, not JSON
	audience      string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	cp := &controlPlane{statuses: map[string][]int{}, calls: map[string]int{}, headers: map[string]http.Header{}, bodies: map[string]string{}, sizes: map[string]int{}, reasons: map[string]string{}, answers: map[string]string{}}
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
		cp.sizes[r.URL.Path] = len(body)
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
		} else if strings.HasPrefix(r.URL.Path, storagePrefix) {
			// A signed URL is the credential: the run token isn't sent there.
			if r.Header.Get("Authorization") != "" {
				status = http.StatusBadRequest
			}
		} else if r.Header.Get("Authorization") != "Bearer "+runToken {
			status = http.StatusUnauthorized
		}
		if status == http.StatusTooManyRequests && cp.retryAfterHeader != "" {
			w.Header().Set("Retry-After", cp.retryAfterHeader)
		}
		w.WriteHeader(status)
		switch {
		case status == http.StatusTooManyRequests && cp.rateLimitBody != "":
			fmt.Fprint(w, cp.rateLimitBody)
		case status == http.StatusTooManyRequests && cp.retryAfterMs > 0:
			fmt.Fprintf(w, `{"error":"rate_limited","message":"Too many requests","retryAfterMs":%d}`, cp.retryAfterMs)
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
		case cp.answers[r.URL.Path] != "":
			fmt.Fprint(w, cp.answers[r.URL.Path])
		case r.URL.Path == sessionPath:
			limits := ""
			if cp.sessionLimits != "" {
				limits = `,"limits":` + cp.sessionLimits
			}
			if cp.sessionExtra != "" {
				limits += "," + cp.sessionExtra
			}
			fmt.Fprintf(w, `{"token":%q,"expires_at":"2026-11-18T10:15:00Z"%s}`, runToken, limits)
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

// layout is which reports the output directory has: an import's, a drift
// check's, the inventories a discover run saved and verify.json.
type layout struct{ report, drift, inventory, verify, linkInventory, evidence bool }

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
	for name, has := range map[string]bool{"report.json": files.report, "drift.json": files.drift, "verify.json": files.verify} {
		if !has {
			continue
		}
		if err := os.WriteFile(filepath.Join(reports, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if files.inventory {
		inventories := filepath.Join(dir, "out", ".infraharvest", "inventory")
		if files.linkInventory {
			// The repository commits a link where the inventories go.
			elsewhere := filepath.Join(dir, "elsewhere")
			if err := os.MkdirAll(elsewhere, 0o755); err != nil {
				t.Fatal(err)
			}
			inventories = filepath.Join(elsewhere, "inventory")
			if err := os.MkdirAll(inventories, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(dir, "out", ".infraharvest")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(inventories, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inventories, "aws-0123456789abcdef.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if files.evidence {
		writeEvidence(t, filepath.Join(dir, "out", "_reports", "evidence"))
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

// discoverEnv is what the Control plane step has after a discover run, with
// env added; verifyEnv, after a verify run.
func discoverEnv(env map[string]string) map[string]string {
	all := map[string]string{"IMPORT": "false", "IMPORT_OUTCOME": "skipped", "DISCOVER": "true", "DISCOVER_OUTCOME": "success"}
	for k, v := range env {
		all[k] = v
	}
	return all
}

func verifyEnv(env map[string]string) map[string]string {
	all := map[string]string{"IMPORT": "false", "IMPORT_OUTCOME": "skipped", "VERIFY": "true", "VERIFY_OUTCOME": "success"}
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

// runDocument returns the run document the control plane received.
func (cp *controlPlane) runDocument(t *testing.T) parsed {
	t.Helper()
	var p parsed
	if err := json.Unmarshal([]byte(cp.bodies["/api/v1/ingest/run"]), &p); err != nil {
		t.Fatalf("run: body isn't JSON: %v\n%q", err, cp.bodies["/api/v1/ingest/run"])
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
	if want := "infraharvest-api:" + cp.URL; cp.audience != want {
		t.Errorf("token audience is %q, want %q", cp.audience, want)
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

// The default audience is the origin of the control plane's URL, so a token for
// one control plane is no good at another: scheme and host in lower case, the
// port unless it is the scheme's own, no path. The session request that follows
// goes nowhere (nothing listens at these ports): only the token request is looked at.
func TestUploadDefaultAudienceNamesTheOriginOfTheURL(t *testing.T) {
	for _, tt := range []struct{ url, want string }{
		{"https://localhost:1", "infraharvest-api:https://localhost:1"},
		{"https://LocalHost:1/base/path/", "infraharvest-api:https://localhost:1"},
		{"https://localhost:8443//", "infraharvest-api:https://localhost:8443"},
		{"https://localhost:443/api", "infraharvest-api:https://localhost"},
		{"https://localhost:0443", "infraharvest-api:https://localhost"},
		{"https://localhost:", "infraharvest-api:https://localhost"},
		{"http://localhost:80/x", "infraharvest-api:http://localhost"},
		{"http://localhost:443", "infraharvest-api:http://localhost:443"},
		{"https://localhost:80", "infraharvest-api:https://localhost:80"},
		{"http://127.0.0.1:1", "infraharvest-api:http://127.0.0.1:1"},
	} {
		t.Run(tt.url, func(t *testing.T) {
			cp := newControlPlane(t)
			upload(t, cp, map[string]string{"URL": tt.url})
			if cp.audience != tt.want {
				t.Errorf("token audience is %q, want %q", cp.audience, tt.want)
			}
		})
	}
}

// Two control planes get tokens for different audiences, and one set in the
// input wins over what the URL gives.
func TestUploadAudienceInputWinsOverTheDefault(t *testing.T) {
	cp := newControlPlane(t)
	upload(t, cp, map[string]string{"URL": "https://localhost:1", "AUDIENCE": "infraharvest-api"})
	if cp.audience != "infraharvest-api" {
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

// The graph has addresses as the OCSF inventory does, so it takes the same
// flags for them: --hash-ids hashes them, --drop-addresses leaves them out.
func TestUploadOfTheGraphPassesTheRedactionFlags(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		want []string
		not  []string
	}{
		{"plain", nil, nil, []string{"--hash-ids", "--drop-addresses"}},
		{"hashed", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123"}, []string{"--hash-ids"}, []string{"--drop-addresses"}},
		{"dropped", map[string]string{"DROP_ADDRESSES": "true"}, []string{"--drop-addresses"}, []string{"--hash-ids"}},
		{"both", map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123", "DROP_ADDRESSES": "true"}, []string{"--hash-ids", "--drop-addresses"}, nil},
		{"not dropped", map[string]string{"DROP_ADDRESSES": "false"}, nil, []string{"--drop-addresses"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			res := upload(t, cp, tt.env)
			if res.err != nil {
				t.Fatalf("upload failed: %v\n%s", res.err, res.output)
			}
			var graph string
			for _, line := range exports(res) {
				if strings.Contains(line, "--schema=graph") {
					graph = line
				}
			}
			if graph == "" {
				t.Fatalf("no graph export: %q", exports(res))
			}
			for _, flag := range tt.want {
				if !strings.Contains(graph, flag) {
					t.Errorf("graph export ran as %q, want %s", graph, flag)
				}
			}
			for _, flag := range tt.not {
				if strings.Contains(graph, flag) {
					t.Errorf("graph export ran as %q, not %s", graph, flag)
				}
			}
		})
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
		{"default audience of a host that has none", map[string]string{"URL": "http://[::1]:8080"}, "set control-plane-audience"},
		{"audience for a host that has no default", map[string]string{"URL": "http://[::1]:8080", "AUDIENCE": "infraharvest-api"}, ""},
		{"default audience", map[string]string{"URL": "https://cp.example.com:8443/base"}, ""},
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
		{"hash with env key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "INFRAHARVEST_EXPORT_KEY": "0123456789abcdef"}, ""},
		{"hash with a short key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "HASH_KEY": "0123456789abcde"}, "at least 16 bytes"},
		{"hash with a short env key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "INFRAHARVEST_EXPORT_KEY": "k"}, "at least 16 bytes"},
		{"hash with a short key between spaces", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "HASH_KEY": "  0123456789abcde \n"}, "at least 16 bytes"},
		{"hash with a 16 byte key", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "HASH_KEY": "0123456789abcdef"}, ""},
		{"hash with a key of 8 characters and 16 bytes", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "HASH_KEY": "éééééééé"}, ""},
		{"hash with a key of 8 characters and 15 bytes", map[string]string{"URL": "https://cp.example.com", "HASH_IDS": "true", "HASH_KEY": "ééééééée"}, "at least 16 bytes"},
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
	// The run has both. The OCSF inventory and the graph have the finish,
	// which is their generated_at, and not the start; the gate has no time.
	for i, want := range [][2]bool{{false, true}, {true, true}, {false, false}, {false, true}} {
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
	// The drift check's own times go to its run document, its finish to the
	// summary, and the tag flags are the inventory's.
	if !strings.Contains(got[0], "--started-at=2026-11-03T10:00:00Z --finished-at=2026-11-03T10:02:00Z") ||
		strings.Contains(got[1], "--started-at") || !strings.Contains(got[1], "--finished-at=2026-11-03T10:02:00Z") || strings.Contains(res.args, "--drop-tags") {
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
	if cmd := cp.runDocument(t).Command; cmd != "drift" {
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
	if cmd := cp.runDocument(t).Command; cmd != "import" {
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

// The graph's and the drift summary's generated_at are the finish of the run
// they are of, as the inventory's is, so that sending the same run again
// changes none of them.
func TestUploadGivesTheGraphAndTheDriftSummaryTheirFinish(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, map[string]string{
		"DRIFT": "accept", "DRIFT_OUTCOME": "success",
		"STARTED_AT": "2026-11-03T09:30:00Z", "FINISHED_AT": "2026-11-03T09:37:00Z",
		"DRIFT_STARTED_AT": "2026-11-03T09:00:00Z", "DRIFT_FINISHED_AT": "2026-11-03T09:05:00Z",
	}, layout{report: true, drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	got := exports(res)
	if len(got) != 5 {
		t.Fatalf("exports: %q", got)
	}
	graph, summary := got[3], got[4]
	if !strings.Contains(graph, "--schema=graph ") || !strings.Contains(graph, "--finished-at=2026-11-03T09:37:00Z") || strings.Contains(graph, "--started-at") {
		t.Errorf("graph export ran as %q, want the import's finish only", graph)
	}
	if !strings.Contains(summary, "--schema=drift-summary ") || !strings.Contains(summary, "--finished-at=2026-11-03T09:05:00Z") || strings.Contains(summary, "--started-at") {
		t.Errorf("drift summary export ran as %q, want the drift check's finish only", summary)
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
			if got := cp.runDocument(t).RequestID; got != "01HZX-7:a.b_c" {
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

func TestUploadOfADiscoverRunSendsTheOCSFInventoryAndTheRun(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, discoverEnv(map[string]string{
		"STARTED_AT": "2026-11-03T09:30:00Z", "FINISHED_AT": "2026-11-03T09:31:00Z", "REQUEST_ID": "req-7",
		"DROP_TAGS": "true", "DROP_ADDRESSES": "true",
	}), layout{inventory: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	got := exports(res)
	if len(got) != 2 || !strings.Contains(got[0], "--schema=ocsf-inventory ") || !strings.Contains(got[1], "--schema=run ") {
		t.Fatalf("exports: %q", got)
	}
	// The inventory takes the redaction flags and the finish, and says the
	// command; the run takes the times, the command and the request.
	for _, want := range []string{"--command=discover", "--drop-tags", "--drop-addresses", "--finished-at=2026-11-03T09:31:00Z"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the OCSF inventory export ran as %q, want %s", got[0], want)
		}
	}
	for _, want := range []string{"--command=discover", "--started-at=2026-11-03T09:30:00Z", "--finished-at=2026-11-03T09:31:00Z", "--request-id=req-7"} {
		if !strings.Contains(got[1], want) {
			t.Errorf("the run export ran as %q, want %s", got[1], want)
		}
	}
	if strings.Contains(got[1], "--drop") {
		t.Errorf("the run export ran as %q", got[1])
	}
	for _, doc := range []string{"gate", "graph", "drift-summary"} {
		if n := cp.count("/api/v1/ingest/" + doc); n != 0 {
			t.Errorf("%s: %d requests, want none", doc, n)
		}
	}
	for _, doc := range []string{"ocsf-inventory", "run"} {
		path := "/api/v1/ingest/" + doc
		if cp.count(path) != 1 || cp.headers[path].Get("Idempotency-Key") != runID+":"+doc {
			t.Errorf("%s: %d requests, Idempotency-Key %q", path, cp.count(path), cp.headers[path].Get("Idempotency-Key"))
		}
	}
	if cmd := cp.runDocument(t).Command; cmd != "discover" {
		t.Errorf("run document's command is %q", cmd)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadOfADiscoverRunThatFailedStillSendsWhatItSaved(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, discoverEnv(map[string]string{"DISCOVER_OUTCOME": "failure"}), layout{inventory: true})
	if res.err != nil || cp.count("/api/v1/ingest/run") != 1 || cp.count("/api/v1/ingest/ocsf-inventory") != 1 {
		t.Errorf("run %d, inventory %d requests: %v\n%s", cp.count("/api/v1/ingest/run"), cp.count("/api/v1/ingest/ocsf-inventory"), res.err, res.output)
	}
}

func TestUploadOfADiscoverRunWithoutAnInventory(t *testing.T) {
	cp := newControlPlane(t)
	// Failed before it saved one: a warning, and the step's failure shows.
	res := uploadIn(t, cp, discoverEnv(map[string]string{"DISCOVER_OUTCOME": "failure"}), layout{report: true})
	if res.err != nil || !strings.Contains(res.output, "::warning::control plane: discover left no inventory") {
		t.Errorf("want a warning and no failure: %v\n%s", res.err, res.output)
	}
	if cp.count(sessionPath) != 0 || res.args != "" || strings.Contains(res.outputs, "control-plane-run-id") {
		t.Errorf("something was sent: %q\n%s", res.args, res.output)
	}
	// After a success it is an error.
	res = uploadIn(t, cp, discoverEnv(nil), layout{report: true})
	if res.err == nil || !strings.Contains(res.output, "::error::control plane: no inventory to send") {
		t.Errorf("want a failure: %v\n%s", res.err, res.output)
	}
}

func TestUploadDoesNotFollowALinkedInventory(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, discoverEnv(nil), layout{inventory: true, linkInventory: true})
	if res.err == nil || !strings.Contains(res.output, "no inventory to send") || cp.count(sessionPath) != 0 {
		t.Errorf("want a failure and no session: %v\n%s", res.err, res.output)
	}
}

func TestUploadOfAVerifyRunSendsTheRunAndTheGate(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, verifyEnv(map[string]string{
		"STARTED_AT": "2026-11-03T09:30:00Z", "FINISHED_AT": "2026-11-03T09:40:00Z", "REQUEST_ID": "req-8", "DROP_TAGS": "true", "HASH_IDS": "true", "HASH_KEY": "0123456789abcdef0123",
	}), layout{verify: true, report: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	got := exports(res)
	if len(got) != 2 || !strings.Contains(got[0], "--schema=run ") || !strings.Contains(got[1], "--schema=gate ") {
		t.Fatalf("exports: %q", got)
	}
	for _, want := range []string{"--command=verify", "--started-at=2026-11-03T09:30:00Z", "--finished-at=2026-11-03T09:40:00Z", "--request-id=req-8"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the run export ran as %q, want %s", got[0], want)
		}
	}
	// The gate has no tags, IDs or times to redact or give.
	if !strings.Contains(got[1], "--command=verify") || strings.Contains(got[1], "--drop") || strings.Contains(got[1], "--hash") || strings.Contains(got[1], "--started-at") || strings.Contains(got[1], "--request-id") {
		t.Errorf("the gate export ran as %q", got[1])
	}
	for _, doc := range []string{"ocsf-inventory", "graph", "drift-summary"} {
		if n := cp.count("/api/v1/ingest/" + doc); n != 0 {
			t.Errorf("%s: %d requests, want none", doc, n)
		}
	}
	for _, doc := range []string{"run", "gate"} {
		path := "/api/v1/ingest/" + doc
		if cp.count(path) != 1 || cp.headers[path].Get("Idempotency-Key") != runID+":"+doc {
			t.Errorf("%s: %d requests, Idempotency-Key %q", path, cp.count(path), cp.headers[path].Get("Idempotency-Key"))
		}
	}
	if cmd := cp.runDocument(t).Command; cmd != "verify" {
		t.Errorf("run document's command is %q", cmd)
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

func TestUploadOfAVerifyRunThatFailedItsChecksStillSends(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, verifyEnv(map[string]string{"VERIFY_OUTCOME": "failure"}), layout{verify: true})
	if res.err != nil || cp.count("/api/v1/ingest/run") != 1 || cp.count("/api/v1/ingest/gate") != 1 {
		t.Errorf("run %d, gate %d requests: %v\n%s", cp.count("/api/v1/ingest/run"), cp.count("/api/v1/ingest/gate"), res.err, res.output)
	}
}

func TestUploadOfAVerifyRunWithoutItsReport(t *testing.T) {
	cp := newControlPlane(t)
	// An import's report is there, but the run is verify's.
	res := uploadIn(t, cp, verifyEnv(map[string]string{"VERIFY_OUTCOME": "failure"}), layout{report: true})
	if res.err != nil || !strings.Contains(res.output, "::warning::control plane: verify left no report") || cp.count(sessionPath) != 0 || res.args != "" {
		t.Errorf("want a warning and no failure: %v\n%s", res.err, res.output)
	}
	res = uploadIn(t, cp, verifyEnv(nil), layout{report: true})
	if res.err == nil || !strings.Contains(res.output, "::error::control plane: no report to send") {
		t.Errorf("want a failure: %v\n%s", res.err, res.output)
	}
}

// A run sends the documents of what ran only: discover and verify steps
// that were skipped leave the import's four.
func TestUploadOfAnImportIgnoresDiscoverAndVerifyFiles(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, map[string]string{"DISCOVER": "false", "DISCOVER_OUTCOME": "skipped", "VERIFY": "false", "VERIFY_OUTCOME": "skipped"}, layout{report: true, inventory: true, verify: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := exports(res); len(got) != 4 || strings.Contains(res.args, "--command") {
		t.Errorf("exports: %q", got)
	}
}

// An import that adds to the roots it finds sends the run document with the
// command incremental, and the other documents as an import sends them.
func TestUploadOfAnIncrementalImportSaysSo(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"COMMAND": "incremental", "STARTED_AT": "2026-11-18T10:00:00Z", "REQUEST_ID": "req-7"})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := cp.runDocument(t).Command; got != "incremental" {
		t.Errorf("command is %q, want incremental", got)
	}
	// What an import sends: the same four documents, in the same order.
	for _, doc := range documents {
		if cp.count("/api/v1/ingest/"+doc) != 1 {
			t.Errorf("%s: %d requests, want 1", doc, cp.count("/api/v1/ingest/"+doc))
		}
	}
	lines := exports(res)
	if len(lines) != len(documents) {
		t.Fatalf("exports: %q", lines)
	}
	for _, line := range lines {
		isRun := strings.Contains(line, "--schema=run")
		if got := strings.Contains(line, "--command=incremental"); got != isRun {
			t.Errorf("--command=incremental is %v for: %s", got, line)
		}
	}
	// The run document keeps the import's times and request ID.
	for _, want := range []string{"--started-at=2026-11-18T10:00:00Z", "--request-id=req-7"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("run export lacks %s: %s", want, lines[1])
		}
	}
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID+"\n") {
		t.Errorf("outputs: %q", res.outputs)
	}
}

// Unless a control plane asked for an incremental import, an import is an
// import, whatever the incremental input is.
func TestUploadOfAPlainImportKeepsTheImportCommand(t *testing.T) {
	for _, incremental := range []string{"", "import"} {
		cp := newControlPlane(t)
		env := map[string]string{}
		if incremental != "" {
			env["COMMAND"] = incremental
		}
		res := upload(t, cp, env)
		if res.err != nil {
			t.Fatalf("COMMAND=%q: upload failed: %v\n%s", incremental, res.err, res.output)
		}
		if got := cp.runDocument(t).Command; got != "import" {
			t.Errorf("COMMAND=%q: command is %q, want import", incremental, got)
		}
		if strings.Contains(res.args, "incremental") {
			t.Errorf("COMMAND=%q: the export has an incremental flag:\n%s", incremental, res.args)
		}
	}
}

// A run that isn't an import has no incremental command, whatever the
// input says: a drift check alone is still the drift check's run.
func TestIncrementalInputDoesNotChangeOtherRuns(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, driftEnv(map[string]string{"COMMAND": "incremental"}), layout{drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if got := cp.runDocument(t).Command; got != "drift" {
		t.Errorf("command is %q, want drift", got)
	}
}

// sizeLine is the log line of a document's size, as the script prints it.
func sizeLine(doc string, json, gzipped int) string {
	return fmt.Sprintf("control plane: the %s document is %d bytes, %d bytes gzipped\n", doc, json, gzipped)
}

// Each document has one log line with its size and its gzip size, which are
// those of what the control plane received, and nothing of its content.
func TestUploadLogsTheSizeOfEachDocument(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	for _, doc := range documents {
		path := "/api/v1/ingest/" + doc
		json, gzipped := len(cp.bodies[path]), cp.sizes[path]
		if json == 0 || gzipped == 0 {
			t.Fatalf("%s: sizes %d, %d", doc, json, gzipped)
		}
		line := sizeLine(doc, json, gzipped)
		if got := strings.Count(res.output, line); got != 1 {
			t.Errorf("%s: %d lines %q in:\n%s", doc, got, line, res.output)
		}
		if got := strings.Count(res.output, "the "+doc+" document is"); got != 1 {
			t.Errorf("%s: %d size lines", doc, got)
		}
	}
	// Nothing of a document's content is in the log: neither a value of it
	// nor the document itself.
	for _, leak := range []string{runID, `"schema"`, `"events"`, "ocsf_version"} {
		if strings.Contains(res.output, leak) {
			t.Errorf("the log holds %q:\n%s", leak, res.output)
		}
	}
}

// The sizes are logged for the documents of any run, and before the first
// document is sent, so that a refusal still has them.
func TestUploadLogsSizesOfOtherRunsAndOfARefusedUpload(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, driftEnv(nil), layout{drift: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	for _, doc := range []string{"run", "drift-summary"} {
		path := "/api/v1/ingest/" + doc
		if line := sizeLine(doc, len(cp.bodies[path]), cp.sizes[path]); !strings.Contains(res.output, line) {
			t.Errorf("%s: no line %q in:\n%s", doc, line, res.output)
		}
	}
	if strings.Contains(res.output, "the graph document is") {
		t.Errorf("a size for a document that wasn't made:\n%s", res.output)
	}

	refused := newControlPlane(t)
	refused.queue("/api/v1/ingest/ocsf-inventory", 400)
	res = upload(t, refused, nil)
	if res.err == nil {
		t.Fatal("a refused upload passed")
	}
	for _, doc := range documents {
		if !strings.Contains(res.output, "the "+doc+" document is") {
			t.Errorf("the refused upload logs no size for %s:\n%s", doc, res.output)
		}
	}
}

// curlShim puts a curl in front of the real one that logs its arguments, one
// line a call, and returns the PATH to run with and the log's path.
func curlShim(t *testing.T) (path, log string) {
	t.Helper()
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl isn't installed")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "curl.log")
	shim := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + curl + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH"), log
}

// closedPortURL is a URL where nothing listens, so that a connection is
// refused.
func closedPortURL(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

// A failure no other try can change, such as a TLS handshake with a server
// that speaks plain HTTP, fails at once.
func TestUploadDoesNotRetryAPermanentCurlFailure(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"URL": strings.Replace(cp.URL, "http://", "https://", 1)})
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if strings.Contains(res.output, "trying again") {
		t.Errorf("a failure that can't pass was tried again:\n%s", res.output)
	}
	if !strings.Contains(res.output, "::error::control plane: opening a session") {
		t.Errorf("no error naming the session:\n%s", res.output)
	}
}

// A refused connection can pass, so it is tried again, three times.
func TestUploadRetriesARefusedConnection(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"URL": closedPortURL(t)})
	if res.err == nil {
		t.Fatalf("upload succeeded:\n%s", res.output)
	}
	if got := strings.Count(res.output, "trying again"); got != 3 {
		t.Errorf("%d retries, want 3:\n%s", got, res.output)
	}
	if !strings.Contains(res.output, "(curl exit 7)") {
		t.Errorf("no curl exit 7:\n%s", res.output)
	}
}

func TestUploadWaitsAsLongAsARateLimitAsks(t *testing.T) {
	t.Run("retryAfterMs in the body", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.retryAfterMs = 1200
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, nil)
		if res.err != nil {
			t.Fatalf("upload failed: %v\n%s", res.err, res.output)
		}
		if !strings.Contains(res.output, "got 429 (curl exit 22); trying again in 1.200s") {
			t.Errorf("didn't wait as asked:\n%s", res.output)
		}
		if got := cp.count("/api/v1/ingest/run"); got != 2 {
			t.Errorf("run: %d requests, want 2", got)
		}
	})
	t.Run("Retry-After header", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.retryAfterHeader = "1"
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, nil)
		if res.err != nil {
			t.Fatalf("upload failed: %v\n%s", res.err, res.output)
		}
		if !strings.Contains(res.output, "got 429 (curl exit 22); trying again in 1.000s") {
			t.Errorf("didn't wait as asked:\n%s", res.output)
		}
	})
	t.Run("a body of several JSON values falls back to the header", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.rateLimitBody = `{"retryAfterMs":5}{"retryAfterMs":6}`
		cp.retryAfterHeader = "1"
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, nil)
		if res.err != nil || !strings.Contains(res.output, "trying again in 1.000s") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
	t.Run("the body wins over the header", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.retryAfterMs = 1000
		cp.retryAfterHeader = "50"
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, nil)
		if res.err != nil || !strings.Contains(res.output, "trying again in 1.000s") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
	t.Run("capped", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.retryAfterMs = 3_600_000
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, map[string]string{"INFRAHARVEST_UPLOAD_MAX_WAIT": "1"})
		if res.err != nil {
			t.Fatalf("upload failed: %v\n%s", res.err, res.output)
		}
		if !strings.Contains(res.output, "trying again in 1.000s") {
			t.Errorf("waited past the cap:\n%s", res.output)
		}
	})
	t.Run("nothing asked", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.queue("/api/v1/ingest/run", 429, 200)
		res := upload(t, cp, map[string]string{"INFRAHARVEST_UPLOAD_RETRY_DELAY": "0"})
		if res.err != nil || !strings.Contains(res.output, "got 429 (curl exit 22); trying again in 0.000s") {
			t.Errorf("didn't fall back to the delay: %v\n%s", res.err, res.output)
		}
	})
}

// --max-time grows with the body: a minute, and 30 seconds for each whole
// megabyte of the gzipped document.
func TestUploadAllowsLargerBodiesMoreTime(t *testing.T) {
	path, log := curlShim(t)
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"PATH": path, "FAKE_PAD_RANDOM": "1400000"})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"/api/v1/session": "--max-time 60 ", "/api/v1/ingest/ocsf-inventory": "--max-time 90 ", "/api/v1/ingest/run": "--max-time 60 "}
	for target, flag := range want {
		found := false
		for _, line := range strings.Split(string(calls), "\n") {
			if strings.HasSuffix(line, target) {
				found = true
				if !strings.Contains(line, flag) {
					t.Errorf("%s: want %q in %q", target, flag, line)
				}
			}
		}
		if !found {
			t.Errorf("no request to %s:\n%s", target, calls)
		}
	}
}

// A document over what the control plane takes fails before anything is sent,
// so that no run is left half uploaded.
func TestUploadRefusesADocumentOverTheLimitsBeforeSendingAny(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
	}{
		{"decompressed", map[string]string{"FAKE_PAD_ZERO": "17000000"}},
		{"compressed", map[string]string{"FAKE_PAD_RANDOM": "1900000"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			res := upload(t, cp, tt.env)
			if res.err == nil {
				t.Fatalf("upload succeeded:\n%s", res.output)
			}
			if !strings.Contains(res.output, "::error::control plane: the ocsf-inventory document is too large") || !strings.Contains(res.output, "nothing was sent") {
				t.Errorf("no clear error:\n%s", res.output)
			}
			for _, doc := range documents {
				if got := cp.count("/api/v1/ingest/" + doc); got != 0 {
					t.Errorf("%s: %d requests before the size check refused", doc, got)
				}
			}
			if strings.Contains(res.outputs, "control-plane-run-id") {
				t.Errorf("outputs: %q", res.outputs)
			}
		})
	}
}

// Over the limit still only warns when the control plane is optional.
func TestUploadOfADocumentOverTheLimitsIsOptionalToo(t *testing.T) {
	cp := newControlPlane(t)
	res := upload(t, cp, map[string]string{"FAKE_PAD_ZERO": "17000000", "OPTIONAL": "true"})
	if res.err != nil || strings.Contains(res.output, "::error::") || cp.count("/api/v1/ingest/run") != 0 {
		t.Errorf("%v\n%s", res.err, res.output)
	}
}

// Within 10% of a limit the document is sent, with a warning.
func TestUploadWarnsOfADocumentNearTheLimits(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
	}{
		{"decompressed", map[string]string{"FAKE_PAD_ZERO": "15000000"}},
		{"compressed", map[string]string{"FAKE_PAD_RANDOM": "1650000"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			res := upload(t, cp, tt.env)
			if res.err != nil {
				t.Fatalf("upload failed: %v\n%s", res.err, res.output)
			}
			if !strings.Contains(res.output, "::warning::control plane: the ocsf-inventory document is") || !strings.Contains(res.output, "within 10%") {
				t.Errorf("no warning:\n%s", res.output)
			}
			if got := cp.count("/api/v1/ingest/ocsf-inventory"); got != 1 {
				t.Errorf("inventory: %d requests, want 1", got)
			}
		})
	}
	cp := newControlPlane(t)
	res := upload(t, cp, nil)
	if res.err != nil || strings.Contains(res.output, "within 10%") {
		t.Errorf("a small estate warned: %v\n%s", res.err, res.output)
	}
}

// The control plane says what it takes in the session answer, and the Action
// holds each document to that instead of its defaults.
func TestUploadTakesTheLimitsOfTheSessionAnswer(t *testing.T) {
	t.Run("lower than the defaults", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.sessionLimits = `{"max_body_bytes":500000,"max_decompressed_bytes":3000000}`
		res := upload(t, cp, map[string]string{"FAKE_PAD_ZERO": "4000000"})
		if res.err == nil {
			t.Fatalf("upload succeeded:\n%s", res.output)
		}
		if !strings.Contains(res.output, "::notice::control plane: it takes a document of 500000 bytes gzipped and 3000000 bytes plain") ||
			!strings.Contains(res.output, "against at most 3000000 and 500000; nothing was sent") {
			t.Errorf("limits not used:\n%s", res.output)
		}
		if got := cp.count("/api/v1/ingest/ocsf-inventory"); got != 0 {
			t.Errorf("%d requests before the size check refused", got)
		}
	})
	t.Run("higher than the defaults", func(t *testing.T) {
		// A document the defaults refuse for its gzipped size.
		cp := newControlPlane(t)
		cp.sessionLimits = `{"max_body_bytes":4000000,"max_decompressed_bytes":20000000}`
		res := upload(t, cp, map[string]string{"FAKE_PAD_RANDOM": "1900000"})
		if res.err != nil {
			t.Fatalf("upload failed: %v\n%s", res.err, res.output)
		}
		if got := cp.count("/api/v1/ingest/ocsf-inventory"); got != 1 {
			t.Errorf("inventory: %d requests, want 1", got)
		}
	})
	t.Run("the warning near them moves with them", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.sessionLimits = `{"max_body_bytes":2000000,"max_decompressed_bytes":4000000}`
		res := upload(t, cp, map[string]string{"FAKE_PAD_ZERO": "3700000"})
		if res.err != nil || !strings.Contains(res.output, "within 10%25 of what the control plane takes (4000000 and 2000000)") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
	t.Run("the defaults when it says nothing", func(t *testing.T) {
		cp := newControlPlane(t)
		res := upload(t, cp, map[string]string{"FAKE_PAD_ZERO": "17000000"})
		if res.err == nil || !strings.Contains(res.output, "against at most 16000000 and 2000000") || strings.Contains(res.output, "session answer") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
}

// Limits that are not two whole numbers in bounds are not believed: the
// defaults stay, with a warning, and the run goes on.
func TestUploadIgnoresMalformedLimitsOfTheSessionAnswer(t *testing.T) {
	for _, tt := range []struct{ name, limits string }{
		{"null", `null`},
		{"a string", `"2000000"`},
		{"an array", `[2000000,16000000]`},
		{"empty", `{}`},
		{"one missing", `{"max_body_bytes":9000000}`},
		{"the other missing", `{"max_decompressed_bytes":90000000}`},
		{"strings", `{"max_body_bytes":"9000000","max_decompressed_bytes":"90000000"}`},
		{"zero", `{"max_body_bytes":0,"max_decompressed_bytes":90000000}`},
		{"negative", `{"max_body_bytes":9000000,"max_decompressed_bytes":-1}`},
		{"fractional", `{"max_body_bytes":9000000.5,"max_decompressed_bytes":90000000}`},
		{"above any sane limit", `{"max_body_bytes":1000000000000,"max_decompressed_bytes":1000000000000}`},
		{"below any sane limit", `{"max_body_bytes":10,"max_decompressed_bytes":10}`},
		{"null members", `{"max_body_bytes":null,"max_decompressed_bytes":null}`},
		{"booleans", `{"max_body_bytes":true,"max_decompressed_bytes":true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A document over the defaults is still refused: the limits were not taken.
			cp := newControlPlane(t)
			cp.sessionLimits = tt.limits
			res := upload(t, cp, map[string]string{"FAKE_PAD_ZERO": "17000000"})
			if res.err == nil || !strings.Contains(res.output, "against at most 16000000 and 2000000") {
				t.Errorf("limits were taken: %v\n%s", res.err, res.output)
			}
			if !strings.Contains(res.output, "::warning::control plane: the session answer's limits aren't two whole numbers of bytes from 1000 to 100000000") {
				t.Errorf("no warning:\n%s", res.output)
			}
			// And an ordinary document goes through, whatever they said.
			cp = newControlPlane(t)
			cp.sessionLimits = tt.limits
			res = upload(t, cp, nil)
			if res.err != nil || cp.count("/api/v1/ingest/ocsf-inventory") != 1 {
				t.Errorf("an ordinary run failed: %v\n%s", res.err, res.output)
			}
		})
	}
}

// An organisation at its cloud-account limit is told so, once, in a warning;
// nothing else about the run changes.
func TestUploadWarnsAtTheCloudAccountLimit(t *testing.T) {
	const marker = "the organisation is at its cloud-account limit"
	entitlement := func(account string) string {
		return `"entitlements":{"import":{"cloud_accounts":` + account + `}}`
	}
	t.Run("at the limit", func(t *testing.T) {
		for _, account := range []string{`{"limit":10,"used":10}`, `{"limit":1,"used":3}`, `{"limit":0,"used":0}`} {
			cp := newControlPlane(t)
			cp.sessionExtra = entitlement(account)
			res := upload(t, cp, nil)
			if res.err != nil || cp.count("/api/v1/ingest/ocsf-inventory") != 1 {
				t.Errorf("%s: the run did not go on: %v\n%s", account, res.err, res.output)
			}
			if strings.Count(res.output, "::warning::control plane: "+marker) != 1 ||
				!strings.Contains(res.output, "new accounts will be refused at upload, while accounts already sent are still accepted") {
				t.Errorf("%s: no warning:\n%s", account, res.output)
			}
		}
	})
	t.Run("with the numbers in it", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.sessionExtra = entitlement(`{"limit":10,"used":12}`)
		res := upload(t, cp, nil)
		if !strings.Contains(res.output, marker+" (12 of 10 used)") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
	t.Run("with limits beside it", func(t *testing.T) {
		cp := newControlPlane(t)
		cp.sessionLimits = `{"max_body_bytes":500000,"max_decompressed_bytes":3000000}`
		cp.sessionExtra = entitlement(`{"limit":2,"used":2}`)
		res := upload(t, cp, nil)
		if res.err != nil || !strings.Contains(res.output, marker) || !strings.Contains(res.output, "it takes a document of 500000 bytes gzipped") {
			t.Errorf("%v\n%s", res.err, res.output)
		}
	})
	for _, tt := range []struct{ name, extra string }{
		{"under the limit", `"entitlements":{"import":{"cloud_accounts":{"limit":10,"used":9}}}`},
		{"no limit", `"entitlements":{"import":{"cloud_accounts":{"limit":null,"used":400}}}`},
		{"no field", `"entitlements":{"import":{}}`},
		{"no entitlements", ``},
		{"empty entitlements", `"entitlements":{}`},
		{"entitlements null", `"entitlements":null`},
		{"entitlements a string", `"entitlements":"unlimited"`},
		{"entitlements an array", `"entitlements":[1,2]`},
		{"import a string", `"entitlements":{"import":"yes"}`},
		{"cloud_accounts null", `"entitlements":{"import":{"cloud_accounts":null}}`},
		{"cloud_accounts a number", `"entitlements":{"import":{"cloud_accounts":5}}`},
		{"cloud_accounts an array", `"entitlements":{"import":{"cloud_accounts":[10,10]}}`},
		{"limit missing", `"entitlements":{"import":{"cloud_accounts":{"used":3}}}`},
		{"used missing", `"entitlements":{"import":{"cloud_accounts":{"limit":3}}}`},
		{"limit a string", `"entitlements":{"import":{"cloud_accounts":{"limit":"3","used":3}}}`},
		{"used a string", `"entitlements":{"import":{"cloud_accounts":{"limit":3,"used":"3"}}}`},
		{"limit true", `"entitlements":{"import":{"cloud_accounts":{"limit":true,"used":3}}}`},
		{"limit negative", `"entitlements":{"import":{"cloud_accounts":{"limit":-1,"used":3}}}`},
		{"fractional", `"entitlements":{"import":{"cloud_accounts":{"limit":2.5,"used":3}}}`},
		{"used null", `"entitlements":{"import":{"cloud_accounts":{"limit":3,"used":null}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp := newControlPlane(t)
			cp.sessionExtra = tt.extra
			res := upload(t, cp, nil)
			if res.err != nil || cp.count("/api/v1/ingest/ocsf-inventory") != 1 {
				t.Errorf("the run did not go on: %v\n%s", res.err, res.output)
			}
			if strings.Contains(res.output, marker) || strings.Contains(res.output, "cloud-account") {
				t.Errorf("warned:\n%s", res.output)
			}
		})
	}
}
