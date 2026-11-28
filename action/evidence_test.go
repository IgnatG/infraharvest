// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package action

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	evidenceCommit      = "0123456789abcdef0123456789abcdef01234567"
	evidenceWorkflowRun = "1234567890"
	signedSecret        = "signed-secret-Zm9vYmFy"
)

// newBundle is a Sigstore bundle in the protobuf-JSON format, with the
// message signature the control plane takes.
const newBundle = `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{},"messageSignature":{"messageDigest":{"algorithm":"SHA2_256","digest":"AA=="},"signature":"AA=="}}`

// legacyBundle is cosign 2's own format, which the control plane refuses.
const legacyBundle = `{"base64Signature":"AA==","cert":"AA==","rekorBundle":{}}`

// fakeCosign writes bundle (COSIGN_BUNDLE) to the file after --bundle, and
// records its arguments and the bytes it was asked to sign.
const fakeCosign = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_COSIGN_LOG"
for a in "$@"; do
  if [[ "${prev-}" == --bundle ]]; then bundle=$a; fi
  prev=$a
done
cp "$a" "$FAKE_COSIGN_SIGNED"
printf '%s\n' "$COSIGN_BUNDLE" > "$bundle"
`

type evidenceRun struct {
	dir     string // the evidence directory
	output  string
	signed  []byte // the bytes cosign was given
	cosign  string // cosign's arguments
	outputs string
	err     error
}

// signEvidence runs evidence.sh with a fake infraharvest and a fake cosign
// in a directory with a report, with env added to the job's variables.
func signEvidence(t *testing.T, bundle string, env map[string]string) evidenceRun {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s isn't installed", tool)
		}
	}
	work := t.TempDir()
	reports := filepath.Join(work, "out", "_reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(reports, "report.json"), `{"report":1}`+"\n")
	write(filepath.Join(reports, "report.md"), "# Report\n")
	write(filepath.Join(work, "bin", "infraharvest"), fakeBinary)
	write(filepath.Join(work, "bin", "cosign"), fakeCosign)
	script, err := filepath.Abs("evidence.sh")
	if err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(work, "github-output")
	signedFile := filepath.Join(work, "signed")
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + work,
		"GITHUB_OUTPUT=" + githubOutput,
		"BINARY=" + filepath.Join(work, "bin", "infraharvest"),
		"COSIGN=" + filepath.Join(work, "bin", "cosign"),
		"OUT=out",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://token.example",
		"GITHUB_REPOSITORY=acme/platform",
		"GITHUB_SHA=" + evidenceCommit,
		"GITHUB_RUN_ID=" + evidenceWorkflowRun,
		"FINISHED_AT=2026-11-28T10:05:00Z",
		"COSIGN_BUNDLE=" + bundle,
		"FAKE_ARGS_LOG=" + filepath.Join(work, "args.log"),
		"FAKE_COSIGN_LOG=" + filepath.Join(work, "cosign.log"),
		"FAKE_COSIGN_SIGNED=" + signedFile,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, runErr := cmd.CombinedOutput()
	r := evidenceRun{dir: filepath.Join(reports, "evidence"), output: string(out), err: runErr}
	r.signed, _ = os.ReadFile(signedFile)
	log, _ := os.ReadFile(filepath.Join(work, "cosign.log"))
	r.cosign = string(log)
	outputs, _ := os.ReadFile(githubOutput)
	r.outputs = string(outputs)
	return r
}

type manifestFile struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Bytes int64  `json:"bytes"`
}

type manifestDoc struct {
	Schema        string         `json:"schema"`
	Kind          string         `json:"kind"`
	Repository    string         `json:"repository"`
	RunID         string         `json:"run_id"`
	WorkflowRunID string         `json:"workflow_run_id"`
	Commit        string         `json:"commit"`
	CreatedAt     string         `json:"created_at"`
	Files         []manifestFile `json:"files"`
}

// sumsNames reads SHA256SUMS as the control plane does: a digest, two
// spaces and a bare name on each line, a final newline, no other line.
func sumsNames(t *testing.T, sums string) map[string]string {
	t.Helper()
	line := regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,127})$`)
	if !strings.HasSuffix(sums, "\n") {
		t.Fatalf("SHA256SUMS has no final newline: %q", sums)
	}
	names := map[string]string{}
	for _, l := range strings.Split(strings.TrimSuffix(sums, "\n"), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil || strings.Contains(m[2], "..") {
			t.Fatalf("SHA256SUMS line %q isn't a digest and a bare name", l)
		}
		if _, dup := names[m[2]]; dup {
			t.Fatalf("SHA256SUMS lists %s twice", m[2])
		}
		names[m[2]] = m[1]
	}
	return names
}

func TestEvidenceIsFlatWithBareNamesAndAManifest(t *testing.T) {
	r := signEvidence(t, newBundle, nil)
	if r.err != nil {
		t.Fatalf("evidence.sh failed: %v\n%s", r.err, r.output)
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%s is a directory: the bundle must be flat", e.Name())
		}
		have = append(have, e.Name())
	}
	want := []string{"SHA256SUMS", "SHA256SUMS.sigstore.json", "gate.json", "graph.json", "inventory.json", "manifest.json", "report.json", "report.md", "run.json"}
	if strings.Join(have, ",") != strings.Join(want, ",") {
		t.Fatalf("files are %v, want %v", have, want)
	}
	sumsText, _ := os.ReadFile(filepath.Join(r.dir, "SHA256SUMS"))
	names := sumsNames(t, string(sumsText))
	for name, digest := range names {
		data, err := os.ReadFile(filepath.Join(r.dir, name))
		if err != nil {
			t.Errorf("SHA256SUMS lists %s, which isn't in the directory", name)
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			t.Errorf("%s: the digest in SHA256SUMS is not the file's", name)
		}
	}
	if _, ok := names["manifest.json"]; !ok {
		t.Error("SHA256SUMS doesn't list manifest.json")
	}
	for _, own := range []string{"SHA256SUMS", "SHA256SUMS.sigstore.json"} {
		if _, ok := names[own]; ok {
			t.Errorf("SHA256SUMS lists %s", own)
		}
	}
	if len(names) != 7 {
		t.Errorf("SHA256SUMS lists %d files, want 7 (the report, the four documents and the manifest)", len(names))
	}
	if r.outputs != "evidence-dir=out/_reports/evidence\n" {
		t.Errorf("outputs are %q", r.outputs)
	}
}

func TestEvidenceManifestNamesTheRunAndEveryOtherFile(t *testing.T) {
	r := signEvidence(t, newBundle, nil)
	if r.err != nil {
		t.Fatalf("evidence.sh failed: %v\n%s", r.err, r.output)
	}
	raw, _ := os.ReadFile(filepath.Join(r.dir, "manifest.json"))
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var m manifestDoc
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("manifest.json: %v\n%s", err, raw)
	}
	if m.Schema != "infraweaver/evidence-manifest/v1" || m.Kind != "import" {
		t.Errorf("schema %q, kind %q", m.Schema, m.Kind)
	}
	if m.Repository != "acme/platform" || m.WorkflowRunID != evidenceWorkflowRun || m.Commit != evidenceCommit {
		t.Errorf("repository %q, workflow_run_id %q, commit %q", m.Repository, m.WorkflowRunID, m.Commit)
	}
	if m.RunID != runID {
		t.Errorf("run_id is %q, want the run document's %q", m.RunID, runID)
	}
	if m.CreatedAt != "2026-11-28T10:05:00Z" {
		t.Errorf("created_at is %q, want the import's finish", m.CreatedAt)
	}
	sumsText, _ := os.ReadFile(filepath.Join(r.dir, "SHA256SUMS"))
	names := sumsNames(t, string(sumsText))
	var listed []string
	for _, f := range m.Files {
		listed = append(listed, f.Name)
		switch f.Role {
		case "sums", "signature", "manifest":
			t.Errorf("%s has the reserved role %q", f.Name, f.Role)
		case "report", "document":
		default:
			t.Errorf("%s has the role %q", f.Name, f.Role)
		}
		if !regexp.MustCompile(`^[a-z]+$`).MatchString(f.Role) {
			t.Errorf("%s: role %q isn't a lower-case word", f.Name, f.Role)
		}
		info, err := os.Stat(filepath.Join(r.dir, f.Name))
		if err != nil || info.Size() != f.Bytes {
			t.Errorf("%s: the manifest says %d bytes", f.Name, f.Bytes)
		}
		if (f.Name == "report.json" || f.Name == "report.md") != (f.Role == "report") {
			t.Errorf("%s has the role %q", f.Name, f.Role)
		}
	}
	// Every SHA256SUMS file but manifest.json, once each.
	var others []string
	for name := range names {
		if name != "manifest.json" {
			others = append(others, name)
		}
	}
	sort.Strings(listed)
	sort.Strings(others)
	if strings.Join(listed, ",") != strings.Join(others, ",") {
		t.Errorf("manifest files are %v, SHA256SUMS lists %v besides the manifest", listed, others)
	}
}

func TestEvidenceSignsTheExactBytesOfSHA256SUMSInTheNewFormat(t *testing.T) {
	r := signEvidence(t, newBundle, nil)
	if r.err != nil {
		t.Fatalf("evidence.sh failed: %v\n%s", r.err, r.output)
	}
	sums, _ := os.ReadFile(filepath.Join(r.dir, "SHA256SUMS"))
	if string(r.signed) != string(sums) {
		t.Errorf("cosign signed %q, SHA256SUMS is %q", r.signed, sums)
	}
	for _, want := range []string{"sign-blob", "--yes", "--new-bundle-format", "--bundle"} {
		if !strings.Contains(r.cosign, want) {
			t.Errorf("cosign was run with %q, want %s", r.cosign, want)
		}
	}
}

func TestEvidenceRefusesALegacyBundleAndLeavesNothingSigned(t *testing.T) {
	r := signEvidence(t, legacyBundle, nil)
	if r.err == nil {
		t.Fatalf("a legacy bundle was accepted:\n%s", r.output)
	}
	if !strings.Contains(r.output, "message-signature bundle") {
		t.Errorf("output lacks the reason:\n%s", r.output)
	}
	if _, err := os.Stat(r.dir); err == nil {
		t.Error("the evidence directory is left behind")
	}
}

func TestEvidenceNeedsTheJobsCommitRunAndRepository(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"commit":     {"GITHUB_SHA": "main"},
		"run":        {"GITHUB_RUN_ID": "abc"},
		"repository": {"GITHUB_REPOSITORY": "platform"},
	} {
		t.Run(name, func(t *testing.T) {
			r := signEvidence(t, newBundle, env)
			if r.err == nil {
				t.Fatalf("signed without a valid %s:\n%s", name, r.output)
			}
			if r.cosign != "" {
				t.Errorf("cosign ran: %s", r.cosign)
			}
		})
	}
}

// writeEvidence writes what evidence.sh does, in dir, for the upload tests.
func writeEvidence(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []struct{ name, role, content string }{
		{"report.json", "report", `{"report":1}` + "\n"},
		{"run.json", "document", `{"run_id":"` + runID + `"}` + "\n"},
	}
	var listed []manifestFile
	var sums strings.Builder
	put := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	for _, f := range files {
		put(f.name, f.content)
		listed = append(listed, manifestFile{Name: f.name, Role: f.role, Bytes: int64(len(f.content))})
	}
	manifest, err := json.Marshal(manifestDoc{
		Schema: "infraweaver/evidence-manifest/v1", Kind: "import", Repository: "acme/platform", RunID: runID,
		WorkflowRunID: evidenceWorkflowRun, Commit: evidenceCommit, CreatedAt: "2026-11-28T10:05:00Z", Files: listed,
	})
	if err != nil {
		t.Fatal(err)
	}
	put("manifest.json", string(manifest)+"\n")
	for name, content := range map[string]string{"SHA256SUMS": sums.String(), "SHA256SUMS.sigstore.json": newBundle + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	evidencePath = "/api/v1/evidence"
	completePath = "/api/v1/evidence/6c1d6c58-0a77-4a7e-9d1a-0123456789ab/complete"
)

// evidenceAnswer is the control plane's answer to the upload: one signed URL
// a file, on the same server, the secret in the query.
func evidenceAnswer(cp *controlPlane, status string) string {
	uploads := ""
	if status == "pending" {
		uploads = fmt.Sprintf(`{"name":"report.json","method":"PUT","url":"%[1]s/storage/report.json?token=%[2]s","content_type":"application/json","bytes":13},`+
			`{"name":"run.json","method":"PUT","url":"%[1]s/storage/run.json?token=%[2]s","content_type":"application/json","bytes":%[3]d},`+
			`{"name":"manifest.json","method":"PUT","url":"%[1]s/storage/manifest.json?token=%[2]s","content_type":"application/json","bytes":0}`, cp.URL, signedSecret, len(`{"run_id":"`+runID+`"}`)+1)
	}
	return fmt.Sprintf(`{"id":"6c1d6c58-0a77-4a7e-9d1a-0123456789ab","status":%q,"uploads":[%s],"complete_path":%q}`, status, uploads, completePath)
}

// uploadEvidence runs the upload of an import whose evidence is in out.
func uploadEvidence(t *testing.T, cp *controlPlane, env map[string]string) result {
	t.Helper()
	all := map[string]string{"EVIDENCE_DIR": "out/_reports/evidence", "GITHUB_RUN_ID": evidenceWorkflowRun, "GITHUB_RUN_ATTEMPT": "1"}
	for k, v := range env {
		all[k] = v
	}
	return uploadIn(t, cp, all, layout{report: true, evidence: true})
}

// plain removes the mask commands, which the runner reads and never shows.
func plain(output string) string {
	var kept []string
	for _, l := range strings.Split(output, "\n") {
		if !strings.HasPrefix(l, "::add-mask::") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func TestEvidenceUploadHappyPath(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 201)
	cp.answers[evidencePath] = evidenceAnswer(cp, "pending")
	cp.answers[completePath] = `{"id":"6c1d6c58-0a77-4a7e-9d1a-0123456789ab","status":"complete","expires_at":"2027-11-28T10:00:00Z"}`
	res := uploadEvidence(t, cp, nil)
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	h := cp.headers[evidencePath]
	if got := h.Get("Authorization"); got != "Bearer "+runToken {
		t.Errorf("Authorization is %q", got)
	}
	if got := h.Get("Idempotency-Key"); got != runID+":evidence:"+evidenceWorkflowRun+":1" {
		t.Errorf("Idempotency-Key is %q", got)
	}
	if h.Get("Content-Type") != "application/json" || h.Get("Content-Encoding") != "gzip" {
		t.Errorf("Content-Type %q, Content-Encoding %q", h.Get("Content-Type"), h.Get("Content-Encoding"))
	}
	// The body has the exact text of the sums and the manifest, and the bundle as JSON.
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cp.bodies[evidencePath]), &body); err != nil {
		t.Fatalf("body isn't JSON: %v", err)
	}
	if len(body) != 3 {
		t.Errorf("body has %d members, want sums, bundle and manifest", len(body))
	}
	var sums, manifest string
	if json.Unmarshal(body["sums"], &sums) != nil || json.Unmarshal(body["manifest"], &manifest) != nil {
		t.Fatalf("sums and manifest aren't strings: %s", cp.bodies[evidencePath])
	}
	var bundle struct {
		MessageSignature json.RawMessage `json:"messageSignature"`
	}
	if err := json.Unmarshal(body["bundle"], &bundle); err != nil || bundle.MessageSignature == nil {
		t.Errorf("bundle is %s, want the Sigstore bundle as JSON", body["bundle"])
	}
	if !strings.HasSuffix(sums, "  manifest.json\n") || !strings.Contains(manifest, `"kind":"import"`) {
		t.Errorf("sums %q, manifest %q", sums, manifest)
	}
	// Each file goes to its signed URL with the given content type, with no run token.
	for _, name := range []string{"report.json", "run.json", "manifest.json"} {
		path := storagePrefix + name
		if cp.count(path) != 1 {
			t.Errorf("%s: %d requests, want 1", path, cp.count(path))
			continue
		}
		if got := cp.headers[path].Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type is %q", path, got)
		}
		if got := cp.headers[path].Get("Authorization"); got != "" {
			t.Errorf("%s: the run token went to the signed URL: %q", path, got)
		}
	}
	if cp.bodies[storagePrefix+"report.json"] != "{\"report\":1}\n" {
		t.Errorf("report.json was sent as %q", cp.bodies[storagePrefix+"report.json"])
	}
	if cp.count(completePath) != 1 || cp.bodies[completePath] != "" {
		t.Errorf("complete: %d requests with body %q, want one with none", cp.count(completePath), cp.bodies[completePath])
	}
	if got := cp.headers[completePath].Get("Authorization"); got != "Bearer "+runToken {
		t.Errorf("complete: Authorization is %q", got)
	}
	if !strings.Contains(res.output, "uploaded the signed evidence") {
		t.Errorf("output lacks the result:\n%s", res.output)
	}
}

func TestEvidenceUploadNeverShowsTheTokensOrSignedURLs(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 201)
	cp.answers[evidencePath] = evidenceAnswer(cp, "pending")
	// One file's PUT fails, then the bundle is refused: the paths that print most.
	cp.queue(storagePrefix+"run.json", 500, 500, 500, 500)
	res := uploadEvidence(t, cp, nil)
	if res.err != nil {
		t.Fatalf("a failed evidence upload failed the step: %v\n%s", res.err, res.output)
	}
	shown := plain(res.output)
	for _, secret := range []string{runToken, oidcToken, oidcRequestToken, signedSecret, "token="} {
		if strings.Contains(shown, secret) {
			t.Errorf("the log shows %q:\n%s", secret, shown)
		}
	}
	// The signed URLs are masked, so a runner hides them wherever they'd show.
	if !strings.Contains(res.output, "::add-mask::"+cp.URL+"/storage/report.json?token="+signedSecret) {
		t.Errorf("a signed URL isn't masked:\n%s", res.output)
	}
	if !strings.Contains(shown, "::warning::") {
		t.Errorf("no warning for the failure:\n%s", shown)
	}
}

func TestEvidenceUploadNoPlanIsANotice(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 402)
	cp.reasons[evidencePath] = "evidence_requires_plan"
	res := uploadEvidence(t, cp, nil)
	if res.err != nil {
		t.Fatalf("a 402 failed the step: %v\n%s", res.err, res.output)
	}
	if cp.count(evidencePath) != 1 {
		t.Errorf("%d requests, a 402 isn't retried", cp.count(evidencePath))
	}
	if !strings.Contains(res.output, "::notice::control plane: the organisation's plan keeps no hosted evidence") || strings.Contains(res.output, "::warning::") {
		t.Errorf("want a notice and no warning:\n%s", res.output)
	}
	if cp.count(storagePrefix+"report.json") != 0 || cp.count(completePath) != 0 {
		t.Error("files were sent after the 402")
	}
	// The documents' result is still reported.
	if !strings.Contains(res.outputs, "control-plane-run-id="+runID) {
		t.Errorf("outputs are %q", res.outputs)
	}
}

func TestEvidenceUploadRefusedBundleWarnsWithTheProblems(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 201)
	cp.answers[evidencePath] = evidenceAnswer(cp, "pending")
	cp.queue(completePath, 422)
	cp.plain = true
	cp.reasons[completePath] = `{"error":"evidence_bundle_refused","problems":[{"name":"run.json","reason":"digest_mismatch"},{"name":"gate.json","reason":"missing"}]}`
	res := uploadEvidence(t, cp, nil)
	if res.err != nil {
		t.Fatalf("a refused bundle failed the step: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.output, "::warning::control plane: the signed evidence wasn't uploaded: the control plane refused the bundle: run.json: digest_mismatch, gate.json: missing") {
		t.Errorf("the warning lacks the problems:\n%s", res.output)
	}
	if cp.count(completePath) != 1 {
		t.Errorf("complete: %d requests, a 422 isn't retried", cp.count(completePath))
	}
}

func TestEvidenceUploadReplayIsTheSameBundle(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 201, 200)
	cp.answers[evidencePath] = evidenceAnswer(cp, "pending")
	cp.answers[completePath] = `{"id":"6c1d6c58-0a77-4a7e-9d1a-0123456789ab","status":"complete","expires_at":"2027-11-28T10:00:00Z"}`
	first := uploadEvidence(t, cp, nil)
	if first.err != nil {
		t.Fatalf("first upload failed: %v\n%s", first.err, first.output)
	}
	key := cp.headers[evidencePath].Get("Idempotency-Key")
	// The replay of the same key is a complete bundle with no uploads.
	cp.answers[evidencePath] = evidenceAnswer(cp, "complete")
	second := uploadEvidence(t, cp, nil)
	if second.err != nil {
		t.Fatalf("replay failed: %v\n%s", second.err, second.output)
	}
	if got := cp.headers[evidencePath].Get("Idempotency-Key"); got != key {
		t.Errorf("the key changed between sends: %q and %q", key, got)
	}
	if cp.count(evidencePath) != 2 || cp.count(storagePrefix+"report.json") != 1 || cp.count(completePath) != 1 {
		t.Errorf("requests: %d uploads, %d PUTs, %d completes; want 2, 1, 1", cp.count(evidencePath), cp.count(storagePrefix+"report.json"), cp.count(completePath))
	}
	if !strings.Contains(second.output, "already complete") {
		t.Errorf("replay output:\n%s", second.output)
	}
}

func TestEvidenceUploadStorageConflictIsFineOnARetry(t *testing.T) {
	cp := newControlPlane(t)
	cp.queue(evidencePath, 201)
	cp.answers[evidencePath] = evidenceAnswer(cp, "pending")
	cp.queue(storagePrefix+"report.json", 409)
	cp.answers[completePath] = `{"id":"x","status":"complete","expires_at":"2027-11-28T10:00:00Z"}`
	res := uploadEvidence(t, cp, nil)
	if res.err != nil || strings.Contains(res.output, "::warning::") {
		t.Fatalf("a 409 from storage failed or warned: %v\n%s", res.err, res.output)
	}
	if cp.count(completePath) != 1 {
		t.Errorf("complete: %d requests, want 1", cp.count(completePath))
	}
}

func TestEvidenceUploadIsOnlyWithTheEvidenceDirectory(t *testing.T) {
	cp := newControlPlane(t)
	res := uploadIn(t, cp, nil, layout{report: true, evidence: true})
	if res.err != nil {
		t.Fatalf("upload failed: %v\n%s", res.err, res.output)
	}
	if cp.count(evidencePath) != 0 {
		t.Errorf("evidence was sent without EVIDENCE_DIR (%d requests)", cp.count(evidencePath))
	}
}

func TestEvidenceUploadRefusalNeverFailsAnOptionalOrRequiredStep(t *testing.T) {
	for _, status := range []int{400, 403, 409, 422, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			cp := newControlPlane(t)
			cp.queue(evidencePath, status)
			cp.reasons[evidencePath] = "signature_invalid"
			res := uploadEvidence(t, cp, map[string]string{"INFRAHARVEST_UPLOAD_MAX_WAIT": "0"})
			if res.err != nil {
				t.Fatalf("status %d failed the step: %v\n%s", status, res.err, res.output)
			}
			if !strings.Contains(res.output, "::warning::control plane: the signed evidence wasn't uploaded") {
				t.Errorf("no warning:\n%s", res.output)
			}
		})
	}
}

func TestEvidenceUploadRefusesAnUploadThatIsNotAPutToAFile(t *testing.T) {
	for name, upload := range map[string]string{
		"path":   `{"name":"../report.json","method":"PUT","url":"%s/storage/x","content_type":"application/json","bytes":1}`,
		"method": `{"name":"report.json","method":"POST","url":"%s/storage/x","content_type":"application/json","bytes":1}`,
		"scheme": `{"name":"report.json","method":"PUT","url":"http://example.com/x","content_type":"application/json","bytes":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			cp := newControlPlane(t)
			cp.queue(evidencePath, 201)
			if strings.Contains(upload, "%s") {
				upload = fmt.Sprintf(upload, cp.URL)
			}
			cp.answers[evidencePath] = fmt.Sprintf(`{"id":"x","status":"pending","uploads":[%s],"complete_path":%q}`, upload, completePath)
			res := uploadEvidence(t, cp, nil)
			if res.err != nil {
				t.Fatalf("the step failed: %v\n%s", res.err, res.output)
			}
			if cp.count(completePath) != 0 || cp.count(storagePrefix+"x") != 0 {
				t.Errorf("went on after a bad upload entry:\n%s", res.output)
			}
			if !strings.Contains(res.output, "::warning::") {
				t.Errorf("no warning:\n%s", res.output)
			}
		})
	}
}
