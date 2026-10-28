// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// conftest's go.mod at a release, as recorded, names the OPA release it
// is built with in its require block; a require directive of its own
// names it too.
func TestOPARequirement(t *testing.T) {
	t.Parallel()
	recorded, err := os.ReadFile(filepath.Join("testdata", "conftest-v0.71.1-go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		goMod   string
		want    string
		wantErr bool
	}{
		"recorded":  {goMod: string(recorded), want: "1.21.1"},
		"directive": {goMod: "module m\n\nrequire github.com/open-policy-agent/opa v1.22.0\n", want: "1.22.0"},
		"indirect":  {goMod: "require (\n\tgithub.com/open-policy-agent/opa v1.23.4 // indirect\n)\n", want: "1.23.4"},
		"other":     {goMod: "require github.com/open-policy-agent/opa-envoy-plugin v1.0.0\n", wantErr: true},
	} {
		got, err := opaRequirement([]byte(test.goMod))
		if got != test.want || (err != nil) != test.wantErr {
			t.Errorf("%s: got %q, %v, want %q", name, got, err, test.want)
		}
	}
}

// The command reads opaVersion and deniedBuiltins out of the gate's
// source as it is.
func TestReadSource(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile(filepath.Join("..", "..", "gate", "conftest_capabilities.go"))
	if err != nil {
		t.Fatal(err)
	}
	opa, denied, err := readSource(content)
	if err != nil {
		t.Fatal(err)
	}
	if !releaseTag.MatchString("v" + opa) {
		t.Errorf("opaVersion: got %q", opa)
	}
	for _, name := range []string{"http.send", "net.lookup_ip_addr", "opa.runtime", "parse_config_file"} {
		if !strings.Contains(strings.Join(denied, " "), name) {
			t.Errorf("deniedBuiltins: got %v, want %s among them", denied, name)
		}
	}
}

// A newer release's functions are listed, those named or marked like the
// denied ones for a look; a denied function the newer release drops is
// called out, while conftest's own, which neither file has, aren't.
func TestCompare(t *testing.T) {
	t.Parallel()
	oldCaps, newCaps := readSample(t, "v1.21.1.json"), readSample(t, "v1.22.0.json")
	got, err := compare(oldCaps, newCaps, []string{"http.send", "opa.runtime", "parse_config"})
	if err != nil {
		t.Fatal(err)
	}
	want := review{
		Added:         []string{"crypto.random_bytes", "net.cidr_split", "strings.title", "yaml.read_file"},
		Suspect:       []string{"crypto.random_bytes", "net.cidr_split", "yaml.read_file"},
		Removed:       []string{"opa.runtime"},
		DroppedDenied: []string{"opa.runtime"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := compare(oldCaps, []byte(`{"builtins": []}`), nil); err == nil {
		t.Error("a file without built-in functions: got no error")
	}
}

// The bump names the newer releases where the prose named the old ones,
// and leaves other versions alone.
func TestRename(t *testing.T) {
	t.Parallel()
	in := "const opaVersion = \"1.21.1\"\n// of OPA 1.21.1, the release conftest 0.71.1 is built with (capabilities/v1.21.1.json)\n// trivy 1.21.1 and conftest 0.71.1 write them\n"
	want := "const opaVersion = \"1.22.0\"\n// of OPA 1.22.0, the release conftest 0.72.0 is built with (capabilities/v1.22.0.json)\n// trivy 1.21.1 and conftest 0.71.1 write them\n"
	if got := string(rename([]byte(in), "1.21.1", "1.22.0", "0.72.0")); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Behind the newest conftest's OPA, the check reports the releases and the
// new functions and writes nothing; with -write it bumps the embedded file
// and every file that names its release, NOTICE through its script. Run
// on a copy of the repository's files, against recorded answers.
func TestRun(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("the NOTICE script needs bash")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := append([]string{capabilitiesFile, noticeScript, "NOTICE"}, namingFiles...)
	gzipped, err := filepath.Glob(filepath.Join(repo, schemametaDir, "*.json.gz"))
	if err != nil || len(gzipped) == 0 {
		t.Fatalf("schema metadata: %v, %v", gzipped, err)
	}
	for _, path := range gzipped {
		rel, _ := filepath.Rel(repo, path)
		files = append(files, rel)
	}
	for _, path := range files {
		content, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	embedded, _, err := readSource(readFile(t, sourceFile))
	if err != nil {
		t.Fatal(err)
	}

	// The newer capabilities are the embedded ones and a function that
	// reaches the network.
	var caps map[string]any
	if err := json.Unmarshal(readFile(t, capabilitiesFile), &caps); err != nil {
		t.Fatal(err)
	}
	caps["builtins"] = append(caps["builtins"].([]any), map[string]any{"name": "http.fetch", "nondeterministic": true})
	newCaps, err := json.MarshalIndent(caps, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goMod := "module github.com/open-policy-agent/conftest\n\nrequire (\n\tgithub.com/open-policy-agent/opa v99.0.0\n)\n"
	answers := map[string]string{
		"/api/repos/open-policy-agent/conftest/releases/latest":        `{"tag_name": "v9.0.0"}`,
		"/raw/open-policy-agent/conftest/v9.0.0/go.mod":                goMod,
		"/raw/open-policy-agent/opa/v99.0.0/capabilities/v99.0.0.json": string(newCaps),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") != (r.Header.Get("Authorization") == "Bearer token") {
			t.Errorf("%s: got Authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()
	s := &source{HTTP: server.Client(), API: server.URL + "/api", Raw: server.URL + "/raw", Token: "token"}

	var out bytes.Buffer
	if err := run(t.Context(), s, &out, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"are of OPA " + embedded + "; conftest 9.0.0, the newest release, is built with OPA 99.0.0", "- `http.fetch`: look at this one"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report doesn't say %q:\n%s", want, out.String())
		}
	}
	if bytes.Equal(readFile(t, capabilitiesFile), newCaps) {
		t.Error("the check wrote the capabilities file")
	}

	out.Reset()
	if err := run(t.Context(), s, &out, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, capabilitiesFile), newCaps) {
		t.Error("-write didn't write the newer capabilities unchanged")
	}
	for path, want := range map[string]string{
		sourceFile:          `const opaVersion = "99.0.0"`,
		"REUSE.toml":        "capabilities/v99.0.0.json",
		"docs/output.md":    "OPA 99.0.0, the release conftest 9.0.0 is built with",
		"docs/catalogue.md": "OPA 99.0.0",
		"NOTICE":            "https://github.com/open-policy-agent/opa/blob/v99.0.0/capabilities/v99.0.0.json",
	} {
		content := string(readFile(t, path))
		if !strings.Contains(content, want) || strings.Contains(content, "OPA "+embedded) {
			t.Errorf("%s doesn't name %q only", path, want)
		}
	}

	// Bumped, it is the newest: nothing to report.
	out.Reset()
	if err := run(t.Context(), s, &out, false); err != nil || out.Len() != 0 {
		t.Errorf("got %q, %v, want no report", out.String(), err)
	}
}

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
