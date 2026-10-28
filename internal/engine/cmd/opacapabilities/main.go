// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command opacapabilities checks the OPA capabilities infraharvest gives
// conftest (internal/engine/gate/conftest_capabilities.json) against the OPA
// release the newest conftest is built with, and with -write bumps them to
// it. Run from the repository root:
//
//	go run ./internal/engine/cmd/opacapabilities        # report a newer OPA release
//	go run ./internal/engine/cmd/opacapabilities -write # and bump the embedded file to it
//
// The report, in Markdown, is empty when the embedded file is of the
// release the newest conftest is built with. Otherwise it names both
// releases and lists the built-in functions the newer one adds, marking
// those that may reach the network or read files, which a person decides
// on: a function that does belongs in deniedBuiltins
// (internal/engine/gate/conftest_capabilities.go). It fails, after the report,
// when the newer release has dropped one of deniedBuiltins that the
// embedded one has: the name to deny may have changed.
//
// -write replaces the embedded file with OPA's capabilities/v<release>.json
// at that release, unchanged, sets opaVersion, names the releases in
// REUSE.toml, the docs and the doc comments, and writes NOTICE with
// .github/scripts/schema-metadata-notice.sh.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
)

// The files a bump writes, from the repository root.
const (
	capabilitiesFile = "internal/engine/gate/conftest_capabilities.json"
	sourceFile       = "internal/engine/gate/conftest_capabilities.go"
	noticeScript     = ".github/scripts/schema-metadata-notice.sh"
	schemametaDir    = "internal/catalogue/schemameta"
)

// namingFiles name the OPA release, and some the conftest release, in
// prose: a bump rewrites them, and fails if one names neither, as the
// prose has then changed under it.
var namingFiles = []string{sourceFile, "REUSE.toml", "docs/output.md", "docs/catalogue.md"}

// Where releases are looked up; tests point them at a server of their own.
const (
	githubAPI = "https://api.github.com"
	githubRaw = "https://raw.githubusercontent.com"
)

func main() {
	write := flag.Bool("write", false, "bump the embedded capabilities to the OPA release the newest conftest is built with")
	flag.Parse()
	if err := checkGitHub(*write); err != nil {
		log.Fatal(err)
	}
}

// checkGitHub runs the check against GitHub, with the token the
// environment gives, if any.
func checkGitHub(write bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s := &source{HTTP: &http.Client{Timeout: time.Minute}, API: githubAPI, Raw: githubRaw, Token: os.Getenv("GITHUB_TOKEN")}
	return run(ctx, s, os.Stdout, write)
}

func run(ctx context.Context, s *source, out io.Writer, write bool) error {
	goSource, err := os.ReadFile(sourceFile)
	if err != nil {
		return err
	}
	embedded, denied, err := readSource(goSource)
	if err != nil {
		return err
	}
	conftest, opa, err := s.newest(ctx)
	if err != nil {
		return err
	}
	have, err := version.NewVersion(embedded)
	if err != nil {
		return fmt.Errorf("%s: opaVersion: %w", sourceFile, err)
	}
	want, err := version.NewVersion(opa)
	if err != nil {
		return fmt.Errorf("conftest %s's go.mod: OPA %w", conftest, err)
	}
	if !want.GreaterThan(have) {
		log.Printf("the embedded capabilities are of OPA %s, the release conftest %s is built with", embedded, conftest)
		return nil
	}
	oldCaps, err := os.ReadFile(capabilitiesFile)
	if err != nil {
		return err
	}
	newCaps, err := s.get(ctx, s.Raw+"/open-policy-agent/opa/v"+opa+"/capabilities/v"+opa+".json")
	if err != nil {
		return err
	}
	review, err := compare(oldCaps, newCaps, denied)
	if err != nil {
		return err
	}
	if err := report(out, embedded, conftest, opa, review); err != nil {
		return err
	}
	if len(review.DroppedDenied) > 0 {
		return fmt.Errorf("OPA %s has none of %s, which deniedBuiltins names: find what they are called now before bumping", opa, strings.Join(review.DroppedDenied, ", "))
	}
	if !write {
		return nil
	}
	return bump(embedded, opa, conftest, newCaps)
}

// source reads conftest's and OPA's releases from GitHub.
type source struct {
	HTTP *http.Client
	// API is GitHub's REST API, Raw where it serves a repository's files
	// at a tag.
	API, Raw string
	// Token, when set, is sent to the API, which allows an anonymous
	// caller few requests an hour.
	Token string
}

// newest returns the newest conftest release and the OPA release it is
// built with, which its go.mod at that release requires.
func (s *source) newest(ctx context.Context) (conftest, opa string, err error) {
	body, err := s.get(ctx, s.API+"/repos/open-policy-agent/conftest/releases/latest")
	if err != nil {
		return "", "", err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", "", fmt.Errorf("conftest's newest release: %w", err)
	}
	if !releaseTag.MatchString(release.TagName) {
		return "", "", fmt.Errorf("conftest's newest release is tagged %q, not v<major>.<minor>.<patch>", release.TagName)
	}
	goMod, err := s.get(ctx, s.Raw+"/open-policy-agent/conftest/"+release.TagName+"/go.mod")
	if err != nil {
		return "", "", err
	}
	opa, err = opaRequirement(goMod)
	if err != nil {
		return "", "", fmt.Errorf("conftest %s's go.mod: %w", release.TagName, err)
	}
	if !releaseTag.MatchString("v" + opa) {
		return "", "", fmt.Errorf("conftest %s requires OPA %q, not a release", release.TagName, opa)
	}
	return strings.TrimPrefix(release.TagName, "v"), opa, nil
}

func (s *source) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	if s.Token != "" && strings.HasPrefix(url, s.API+"/") {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

// releaseTag matches the tag of a release. The releases read go into
// addresses and files, so a tag that isn't one is refused.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// opaModule is the OPA module's path, which conftest requires.
const opaModule = "github.com/open-policy-agent/opa"

// opaRequirement returns the OPA release a go.mod requires, without its v.
// conftest requires it directly, on a line of a require block or a
// require directive of its own.
func opaRequirement(goMod []byte) (string, error) {
	for line := range strings.SplitSeq(string(goMod), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) >= 2 && fields[0] == opaModule && strings.HasPrefix(fields[1], "v") {
			return strings.TrimPrefix(fields[1], "v"), nil
		}
	}
	return "", fmt.Errorf("it requires no %s", opaModule)
}

// readSource returns opaVersion and deniedBuiltins as the gate's source
// declares them: the command reads them there, rather than import the
// gate, as it rewrites the same file.
func readSource(goSource []byte) (opa string, denied []string, err error) {
	file, err := parser.ParseFile(token.NewFileSet(), sourceFile, goSource, 0)
	if err != nil {
		return "", nil, err
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			switch value.Names[0].Name {
			case "opaVersion":
				if opa, err = stringLiteral(value.Values[0]); err != nil {
					return "", nil, fmt.Errorf("opaVersion: %w", err)
				}
			case "deniedBuiltins":
				list, ok := value.Values[0].(*ast.CompositeLit)
				if !ok {
					return "", nil, errors.New("deniedBuiltins isn't a list of names")
				}
				for _, elt := range list.Elts {
					name, err := stringLiteral(elt)
					if err != nil {
						return "", nil, fmt.Errorf("deniedBuiltins: %w", err)
					}
					denied = append(denied, name)
				}
			}
		}
	}
	if opa == "" || len(denied) == 0 {
		return "", nil, fmt.Errorf("%s declares no opaVersion or deniedBuiltins", sourceFile)
	}
	return opa, denied, nil
}

func stringLiteral(expr ast.Expr) (string, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", errors.New("not a string literal")
	}
	return strconv.Unquote(lit.Value)
}

// review is what a newer OPA release changes in the built-in functions.
type review struct {
	// Added are the functions the newer release has and the embedded one
	// doesn't, and Suspect those of them that may reach the network or
	// read files.
	Added, Suspect []string
	// Removed are the functions the embedded release has and the newer one
	// doesn't, and DroppedDenied those of them deniedBuiltins names.
	Removed, DroppedDenied []string
}

// builtin is what compare reads of a built-in function in a capabilities
// file.
type builtin struct {
	Name string `json:"name"`
	// Nondeterministic is OPA's mark of a function whose result isn't
	// decided by its arguments: those that reach the network are, with
	// clocks and random numbers.
	Nondeterministic bool `json:"nondeterministic"`
}

// compare reviews the built-in functions of the newer capabilities file
// against the embedded one's. deniedBuiltins names conftest's own
// functions too, which neither file has: only those the embedded file has
// and the newer one doesn't are dropped.
func compare(oldCaps, newCaps []byte, denied []string) (review, error) {
	before, err := builtins(oldCaps)
	if err != nil {
		return review{}, fmt.Errorf("the embedded capabilities: %w", err)
	}
	after, err := builtins(newCaps)
	if err != nil {
		return review{}, fmt.Errorf("the newer capabilities: %w", err)
	}
	var r review
	for name, b := range after {
		if _, ok := before[name]; !ok {
			r.Added = append(r.Added, name)
			if suspect(b) {
				r.Suspect = append(r.Suspect, name)
			}
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			r.Removed = append(r.Removed, name)
			if slices.Contains(denied, name) {
				r.DroppedDenied = append(r.DroppedDenied, name)
			}
		}
	}
	for _, names := range [][]string{r.Added, r.Suspect, r.Removed, r.DroppedDenied} {
		slices.Sort(names)
	}
	return r, nil
}

func builtins(caps []byte) (map[string]builtin, error) {
	var file struct {
		Builtins []builtin `json:"builtins"`
	}
	if err := json.Unmarshal(caps, &file); err != nil {
		return nil, err
	}
	if len(file.Builtins) == 0 {
		return nil, errors.New("no built-in functions")
	}
	byName := make(map[string]builtin, len(file.Builtins))
	for _, b := range file.Builtins {
		byName[b.Name] = b
	}
	return byName, nil
}

// suspectPrefixes and suspectWords mark the names of functions in the
// classes deniedBuiltins keeps from policies: those that reach the
// network (http.send, net.lookup_ip_addr), read files or the environment
// (opa.runtime), or load a schema's $ref (json.match_schema). They mark
// names to look at, not to deny: net.cidr_contains reads only its
// arguments.
var (
	suspectPrefixes = []string{"http.", "net.", "opa."}
	suspectWords    = []string{"file", "schema", "env", "fetch", "load", "read"}
)

func suspect(b builtin) bool {
	if b.Nondeterministic {
		return true
	}
	for _, p := range suspectPrefixes {
		if strings.HasPrefix(b.Name, p) {
			return true
		}
	}
	for _, w := range suspectWords {
		if strings.Contains(b.Name, w) {
			return true
		}
	}
	return false
}

// report writes, in Markdown, that the embedded capabilities are behind,
// and the review of the newer release's built-in functions.
func report(out io.Writer, embedded, conftest, opa string, r review) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "The OPA capabilities infraharvest gives conftest (`%s`) are of OPA %s; conftest %s, the newest release, is built with OPA %s. ", capabilitiesFile, embedded, conftest, opa)
	b.WriteString("Until they are bumped, a policy that calls a built-in function newer than OPA " + embedded + " doesn't compile.\n")
	if len(r.Added) > 0 {
		fmt.Fprintf(&b, "\nBuilt-in functions OPA %s adds. Policies may call each, unless `deniedBuiltins` in `%s` names it: one that reaches the network, reads files or the environment, or loads a schema belongs there.\n\n", opa, sourceFile)
		for _, name := range r.Added {
			if slices.Contains(r.Suspect, name) {
				fmt.Fprintf(&b, "- `%s`: look at this one, as its name or its nondeterministic mark is that of those denied\n", name)
			} else {
				fmt.Fprintf(&b, "- `%s`\n", name)
			}
		}
	}
	if len(r.Removed) > 0 {
		fmt.Fprintf(&b, "\nBuilt-in functions OPA %s no longer has:\n\n", opa)
		for _, name := range r.Removed {
			if slices.Contains(r.DroppedDenied, name) {
				fmt.Fprintf(&b, "- `%s`, which `deniedBuiltins` names: find what it is called now\n", name)
			} else {
				fmt.Fprintf(&b, "- `%s`\n", name)
			}
		}
	}
	_, err := out.Write(b.Bytes())
	return err
}

// conftestRelease is how the prose names the conftest release the
// capabilities' OPA release is that of.
var conftestRelease = regexp.MustCompile(`conftest \d+\.\d+\.\d+ is built with`)

// bump writes the newer capabilities file, unchanged, and names its
// release, and the conftest release built with it, where the old ones
// were named.
func bump(oldOPA, newOPA, conftest string, newCaps []byte) error {
	log.Printf("writing %s", capabilitiesFile)
	if err := os.WriteFile(capabilitiesFile, newCaps, 0o644); err != nil {
		return err
	}
	for _, path := range namingFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		renamed := rename(content, oldOPA, newOPA, conftest)
		if bytes.Equal(renamed, content) {
			return fmt.Errorf("%s names neither OPA %s nor a conftest release: name OPA %s there by hand", path, oldOPA, newOPA)
		}
		log.Printf("writing %s", path)
		if err := os.WriteFile(path, renamed, 0o644); err != nil {
			return err
		}
	}
	var notice bytes.Buffer
	cmd := exec.Command("bash", noticeScript, schemametaDir)
	cmd.Stdout, cmd.Stderr = &notice, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", noticeScript, err)
	}
	log.Print("writing NOTICE")
	return os.WriteFile("NOTICE", notice.Bytes(), 0o644)
}

// rename names newOPA where content names oldOPA (as OPA <release>, the
// file v<release>.json or the opaVersion constant), and conftest as the
// release built with it.
func rename(content []byte, oldOPA, newOPA, conftest string) []byte {
	s := strings.NewReplacer(
		"OPA "+oldOPA, "OPA "+newOPA,
		"v"+oldOPA+".json", "v"+newOPA+".json",
		"const opaVersion = "+strconv.Quote(oldOPA), "const opaVersion = "+strconv.Quote(newOPA),
	).Replace(string(content))
	return []byte(conftestRelease.ReplaceAllLiteralString(s, "conftest "+conftest+" is built with"))
}
