// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// optionsAccount is the account testdata/aws-options creates its
// resources in, which no other test uses.
const optionsAccount = "444444444444"

// optionsRegion is the region the options tests import, besides the
// global services.
const optionsRegion = "us-east-1"

// optionsRoot is the root of the account's region in the default layout,
// relative to the output directory.
const optionsRoot = "aws/" + optionsAccount + "/" + optionsRegion

// optionsGlobalRoot is the root of the account's global services in the
// default layout, relative to the output directory.
const optionsGlobalRoot = "aws/" + optionsAccount + "/global"

// optionsEstate is testdata/aws-options, created in the AWS emulator, and
// the engine a test imports it with.
type optionsEstate struct {
	ctx                  context.Context
	engineName, execPath string
	pluginCache          string
	state                []*tfjson.StateResource
}

// optionsSeeded is what seedOptions created in this process. The emulator
// can't create testdata/aws-options twice, so the options tests run in
// one process share it; CI runs them on two runners, each with a Floci of
// its own (see the e2e-aws job).
var optionsSeeded struct {
	sync.Mutex
	state []*tfjson.StateResource
}

// seedOptions creates testdata/aws-options in the AWS emulator at
// AWS_ENDPOINT_URL, with Terraform (or OpenTofu with E2E_ENGINE=tofu),
// unless an earlier test did, skipping the test when there is no
// emulator. The test's imports and plans then sign in to the estate's
// account.
func seedOptions(t *testing.T) optionsEstate {
	t.Helper()
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	pluginCache := filepath.Join(cache, "plugins")
	optionsSeeded.Lock()
	defer optionsSeeded.Unlock()
	if optionsSeeded.state == nil {
		optionsSeeded.state = seed(ctx, t, "aws-options", execPath, pluginCache)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", optionsAccount)
	return optionsEstate{ctx: ctx, engineName: engineName, execPath: execPath, pluginCache: pluginCache, state: optionsSeeded.state}
}

// id returns the ID of the resource of testdata/aws-options with this
// type and name.
func (e optionsEstate) id(t *testing.T, resourceType, name string) string {
	t.Helper()
	for _, r := range e.state {
		if r.Type == resourceType && r.Name == name {
			if id, ok := r.AttributeValues["id"].(string); ok && id != "" {
				return id
			}
		}
	}
	t.Fatalf("testdata/aws-options created no %s.%s", resourceType, name)
	return ""
}

// importAWS runs infraharvest import aws --all with args on the estate's
// account, in optionsRegion and the global services, into a new directory
// it returns with what the import printed on stdout and logged. It fails
// the test unless the import exits with want.
func (e optionsEstate) importAWS(t *testing.T, want int, args ...string) (out, stdout string, logged *stepLog) {
	t.Helper()
	out = t.TempDir()
	stdout, logged, err := optionsRun(e.ctx, t, append([]string{
		"import", "aws",
		"--engine=" + e.engineName,
		"--terraform-path=" + e.execPath,
		"--regions=" + optionsRegion,
		"--out=" + out,
		"--all",
	}, args...)...)
	if got := importer.ExitCode(err); got != want {
		t.Fatalf("infraharvest import aws %s: exit code %d, want %d: %v", strings.Join(args, " "), got, want, err)
	}
	return out, stdout, logged
}

// planAll plans every root under out with checkNoChanges and returns how
// many resources of each type they import.
func (e optionsEstate) planAll(t *testing.T, out string) map[string]int {
	t.Helper()
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(e.ctx, t, dir, e.execPath, e.pluginCache, nil) {
			imported[typ] += n
		}
	}
	return imported
}

// optionsRun runs infraharvest with args, and returns what it printed on
// stdout, what it logged and its error. An import prints its --format
// json report on the process's stdout, so that is a file for the length
// of the run.
func optionsRun(ctx context.Context, t *testing.T, args ...string) (string, *stepLog, error) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	logged := &stepLog{}
	runErr := func() error {
		stdout, logs := os.Stdout, log.Writer()
		os.Stdout = f
		log.SetOutput(io.MultiWriter(logs, logged))
		defer func() {
			os.Stdout = stdout
			log.SetOutput(logs)
		}()
		root := cli.NewCmdRoot()
		root.SetArgs(args)
		return root.ExecuteContext(ctx)
	}()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return readFile(t, name), logged, runErr
}

// optionsReport reads the report an import wrote into out.
func optionsReport(t *testing.T, out string) *report.Report {
	t.Helper()
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// optionsRoots returns the roots under out, by their path relative to it
// with forward slashes.
func optionsRoots(t *testing.T, out string) map[string]string {
	t.Helper()
	roots := map[string]string{}
	for _, dir := range generatedDirs(t, out) {
		rel, err := filepath.Rel(out, dir)
		if err != nil {
			t.Fatal(err)
		}
		roots[filepath.ToSlash(rel)] = dir
	}
	return roots
}

// optionsImports returns the IDs the import blocks of the roots under out
// import, by resource type, sorted.
func optionsImports(t *testing.T, out string) map[string][]string {
	t.Helper()
	ids := map[string][]string{}
	for _, dir := range generatedDirs(t, out) {
		for typ, list := range optionsRootImports(t, dir) {
			ids[typ] = append(ids[typ], list...)
		}
	}
	for _, list := range ids {
		sort.Strings(list)
	}
	return ids
}

// optionsRootImports returns the IDs the import blocks of the root in dir
// import, by resource type: the type of the address each imports to,
// within a module call or not.
func optionsRootImports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	name := filepath.Join(dir, engine.ImportsFileName)
	f, diags := hclsyntax.ParseConfig([]byte(readFile(t, name)), name, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	ids := map[string][]string{}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "import" {
			continue
		}
		toAttr, idAttr := b.Body.Attributes["to"], b.Body.Attributes["id"]
		if toAttr == nil || idAttr == nil {
			t.Errorf("%s: an import block without to or id", name)
			continue
		}
		to, ok := toAttr.Expr.(*hclsyntax.ScopeTraversalExpr)
		if !ok {
			t.Errorf("%s: an import block's to isn't an address", name)
			continue
		}
		// module.<name>.<type>.<name>, with indexes, or <type>.<name>.
		names := []string{to.Traversal.RootName()}
		for _, step := range to.Traversal[1:] {
			if attr, ok := step.(hcl.TraverseAttr); ok {
				names = append(names, attr.Name)
			}
		}
		i := 0
		for i+2 < len(names) && names[i] == "module" {
			i += 2
		}
		id, diags := idAttr.Expr.Value(nil)
		if diags.HasErrors() || id.Type() != cty.String {
			t.Errorf("%s: the import block of %s has no literal ID", name, strings.Join(names, "."))
			continue
		}
		ids[names[i]] = append(ids[names[i]], id.AsString())
	}
	return ids
}

// readsVPC matches an argument that reads the VPC through a data source.
var readsVPC = regexp.MustCompile(`vpc_id\s*=\s*data\.aws_vpc\.\w+\.id`)

// TestAWSOptionsLayout imports with a --layout that puts each service of
// an account and region in a root of its own: the roots are where the
// layout says, each with its own service's resources; the subnet and the
// security group read the VPC, which another root manages, through a
// data source; the default --modules (registry) makes each bucket a call
// of the curated s3-bucket module at the version its adapter is tested
// with, and the roles calls of one generated local module, which the
// deeper roots reach under the output's modules directory; and every root
// plans as imports with no changes.
func TestAWSOptionsLayout(t *testing.T) {
	e := seedOptions(t)
	out, _, _ := e.importAWS(t, report.ExitOK, "--services=vpc,subnet,sg,iam,s3", "--layout={out}/{provider}/{account}/{region}/{service}/")

	regional := optionsRoot + "/"
	want := map[string][]string{
		optionsGlobalRoot + "/iam": {"aws_iam_role", "aws_iam_role_policy"},
		regional + "vpc":           {"aws_vpc"},
		regional + "subnet":        {"aws_subnet"},
		regional + "sg":            {"aws_security_group", "aws_vpc_security_group_ingress_rule"},
		regional + "s3":            {"aws_s3_bucket", "aws_s3_bucket_versioning"},
	}
	home := map[string]string{}
	for rel, types := range want {
		for _, typ := range types {
			home[typ] = rel
		}
	}
	roots := optionsRoots(t, out)
	for rel, dir := range roots {
		if _, ok := want[rel]; !ok {
			t.Errorf("a root at %s, which the layout doesn't make", rel)
		}
		for typ := range optionsRootImports(t, dir) {
			if h, ok := home[typ]; ok && h != rel {
				t.Errorf("%s imports %s, which the layout puts in %s", rel, typ, h)
			}
		}
	}
	for rel, types := range want {
		dir, ok := roots[rel]
		if !ok {
			t.Errorf("no root at %s", rel)
			continue
		}
		imported := checkNoChanges(e.ctx, t, dir, e.execPath, e.pluginCache, nil)
		for _, typ := range types {
			if imported[typ] == 0 {
				t.Errorf("%s imports %v, want %s", rel, imported, typ)
			}
		}
	}

	vpc := e.id(t, "aws_vpc", "main")
	for _, service := range []string{"subnet", "sg"} {
		dir, ok := roots[regional+service]
		if !ok {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, engine.DataFileName))
		if generated := configuration(t, dir); !readsVPC.MatchString(generated) || !strings.Contains(string(data), `data "aws_vpc"`) || !strings.Contains(string(data), vpc) {
			t.Errorf("%s doesn't read the VPC %s of the root %svpc through a data source:\n%s", regional+service, vpc, regional, generated)
		}
	}

	r := optionsReport(t, out)
	buckets, roles := 0, 0
	for _, d := range r.Directories {
		for _, m := range d.Modules {
			if m.Declined != "" {
				t.Logf("%s: %s declined for %v: %s", d.Path, m.Source, m.Resources, m.Declined)
				continue
			}
			switch {
			case m.Source == adapters.S3Bucket().Source:
				buckets++
				if d.Path != regional+"s3" || m.Version != adapters.S3Bucket().Version {
					t.Errorf("%s calls %s %s, want it called at %s from %ss3", d.Path, m.Source, m.Version, adapters.S3Bucket().Version, regional)
				}
			case strings.HasPrefix(path.Join(d.Path, m.Source), engine.ModulesDirName+"/iam_role_"):
				roles++
			default:
				// Such as the VPC, alone in its root, as a call of the vpc
				// module.
				t.Logf("%s calls %s %s for %v", d.Path, m.Source, m.Version, m.Resources)
			}
		}
	}
	if buckets != 2 || roles != 2 {
		t.Errorf("%d calls of %s and %d of a generated IAM role module, want 2 of each", buckets, adapters.S3Bucket().Source, roles)
	}
	if modules, err := filepath.Glob(filepath.Join(out, engine.ModulesDirName, "iam_role_*", "main.tf")); err != nil || len(modules) != 1 {
		t.Errorf("generated IAM role modules: %v (%v), want one", modules, err)
	}
	// The report names a newer release of the module than the one called.
	releases, err := engine.ModuleReleases(e.ctx, http.DefaultClient, "", adapters.S3Bucket().Source)
	if err != nil {
		t.Fatalf("look up the releases of %s: %v", adapters.S3Bucket().Source, err)
	}
	if newest := releases[0].Version.Original(); newest != adapters.S3Bucket().Version {
		newer := report.ModuleVersion{Source: adapters.S3Bucket().Source, Version: adapters.S3Bucket().Version, Latest: newest}
		if !slices.Contains(r.Modules, newer) {
			t.Errorf("the report's modules %+v don't name %s %s, newer than %s", r.Modules, newer.Source, newest, newer.Version)
		}
	}
}

// TestAWSOptionsModules checks the other --modules modes by their effect
// on the output, and verify on a generated output. none keeps every
// resource in the roots, with no module call and no generated module, and
// verify passes on its output, then fails with exit code 1 once a
// generated file no longer matches the cloud (see checkVerify); local
// makes the roles calls of a generated local module and calls no registry
// module; latest-untested calls the newest release of the s3-bucket
// module, not the tested one, and warns that it does. Every root plans as
// imports with no changes.
func TestAWSOptionsModules(t *testing.T) {
	e := seedOptions(t)

	none, _, _ := e.importAWS(t, report.ExitOK, "--services=s3,iam,sqs", "--modules=none")
	if _, err := os.Stat(filepath.Join(none, engine.ModulesDirName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--modules=none wrote %s (%v)", engine.ModulesDirName, err)
	}
	for rel, dir := range optionsRoots(t, none) {
		for _, b := range configBlocks(t, dir) {
			if b.Type == "module" {
				t.Errorf("--modules=none: %s calls the module %s", rel, strings.Join(b.Labels, " "))
			}
		}
		if imports := readFile(t, filepath.Join(dir, engine.ImportsFileName)); strings.Contains(imports, "module.") {
			t.Errorf("--modules=none: %s imports into a module:\n%s", rel, imports)
		}
	}
	for _, d := range optionsReport(t, none).Directories {
		for _, m := range d.Modules {
			if m.Declined == "" {
				t.Errorf("--modules=none: %s calls %s %s", d.Path, m.Source, m.Version)
			}
		}
	}
	e.checkVerify(t, none)
	imported := e.planAll(t, none)
	for typ, want := range map[string]int{"aws_s3_bucket": 2, "aws_s3_bucket_versioning": 2, "aws_iam_role": 2, "aws_iam_role_policy": 2, "aws_sqs_queue": 2} {
		if imported[typ] != want {
			t.Errorf("--modules=none: the roots import %d %s, want %d", imported[typ], typ, want)
		}
	}

	local, _, _ := e.importAWS(t, report.ExitOK, "--services=s3,iam", "--modules=local")
	roles := 0
	for _, d := range optionsReport(t, local).Directories {
		for _, m := range d.Modules {
			if m.Declined != "" {
				continue
			}
			if m.Version != "" || (!strings.HasPrefix(m.Source, "./") && !strings.HasPrefix(m.Source, "../")) {
				t.Errorf("--modules=local: %s calls %s %s, not a generated local module", d.Path, m.Source, m.Version)
			}
			if strings.HasPrefix(path.Join(d.Path, m.Source), engine.ModulesDirName+"/iam_role_") {
				roles++
			}
		}
	}
	if roles != 2 {
		t.Errorf("--modules=local: %d calls of a generated IAM role module, want 2", roles)
	}
	if imported := e.planAll(t, local); imported["aws_iam_role"] != 2 || imported["aws_s3_bucket"] != 2 {
		t.Errorf("--modules=local: the roots import %v, want the roles and the buckets", imported)
	}

	releases, err := engine.ModuleReleases(e.ctx, http.DefaultClient, "", adapters.S3Bucket().Source)
	if err != nil {
		t.Fatalf("look up the releases of %s: %v", adapters.S3Bucket().Source, err)
	}
	newest := releases[0].Version.Original()
	latest, _, logged := e.importAWS(t, report.ExitOK, "--services=s3", "--modules=latest-untested")
	warned := false
	for _, entry := range logged.entries {
		warned = warned || strings.Contains(entry.message, "--modules "+importer.ModulesLatestUntested+" calls module releases no adapter is tested with")
	}
	if !warned {
		t.Error("--modules=latest-untested doesn't warn that it calls untested module releases")
	}
	r := optionsReport(t, latest)
	clusters := 0
	for _, d := range r.Directories {
		for _, m := range d.Modules {
			if m.Source != adapters.S3Bucket().Source {
				continue
			}
			clusters++
			if m.Declined != "" {
				// The plan check still decides; the buckets then stay
				// resources of the root.
				t.Logf("--modules=latest-untested: %s %s declined for %v: %s", m.Source, m.Version, m.Resources, m.Declined)
				continue
			}
			if m.Version != newest {
				t.Errorf("--modules=latest-untested: %s calls %s %s, want its newest release %s", d.Path, m.Source, m.Version, newest)
			}
		}
	}
	if clusters != 2 {
		t.Errorf("--modules=latest-untested: %d buckets tried as calls of %s, want 2", clusters, adapters.S3Bucket().Source)
	}
	for _, m := range r.Modules {
		if m.Source == adapters.S3Bucket().Source {
			t.Errorf("--modules=latest-untested: the report names %s %s, newer than the release called", m.Source, m.Latest)
		}
	}
	if imported := e.planAll(t, latest); imported["aws_s3_bucket"] != 2 || imported["aws_s3_bucket_versioning"] != 2 {
		t.Errorf("--modules=latest-untested: the roots import %v, want the buckets", imported)
	}
}

// retention matches the orders queue's message retention, which
// testdata/aws-options sets.
var retention = regexp.MustCompile(`(message_retention_seconds\s*=\s*)86400\b`)

// checkVerify runs infraharvest verify on out: every root passes as
// generated, and once the file of the region's root that sets the orders
// queue's retention says another, the root fails the plan check and
// verify exits with 1, while the global root still passes. It puts the
// file back as it was.
func (e optionsEstate) checkVerify(t *testing.T, out string) {
	t.Helper()
	verify := func() (map[string]string, string, error) {
		t.Helper()
		stdout, _, err := optionsRun(e.ctx, t, "verify", "--engine="+e.engineName, "--terraform-path="+e.execPath, out)
		// One line per root: "<path>: <status>".
		statuses := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			if root, status, ok := strings.Cut(line, ": "); ok {
				statuses[root] = status
			}
		}
		return statuses, stdout, err
	}
	statuses, stdout, err := verify()
	if err != nil {
		t.Errorf("infraharvest verify of the generated output: %v\n%s", err, stdout)
	}
	for _, root := range []string{optionsRoot, optionsGlobalRoot} {
		if !strings.HasPrefix(statuses[root], "passed") {
			t.Errorf("infraharvest verify of the generated output: %s didn't pass:\n%s", root, stdout)
		}
	}

	dir := filepath.Join(out, filepath.FromSlash(optionsRoot))
	edited := ""
	for _, p := range configPaths(t, dir) {
		if retention.MatchString(readFile(t, p)) {
			edited = p
		}
	}
	if edited == "" {
		t.Errorf("%s: no file sets the orders queue's retention:\n%s", optionsRoot, configuration(t, dir))
		return
	}
	original := readFile(t, edited)
	if err := os.WriteFile(edited, []byte(retention.ReplaceAllString(original, "${1}86401")), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.WriteFile(edited, []byte(original), 0o644); err != nil {
			t.Error(err)
		}
	}()
	statuses, stdout, err = verify()
	if code := importer.ExitCode(err); code != report.ExitIncomplete {
		t.Errorf("infraharvest verify after a change to %s: exit code %d, want %d: %v", edited, code, report.ExitIncomplete, err)
	}
	if status := statuses[optionsRoot]; !strings.HasPrefix(status, "failed") || !strings.Contains(status, engine.CheckPlan) {
		t.Errorf("infraharvest verify after a change to %s: %s didn't fail %s:\n%s", edited, optionsRoot, engine.CheckPlan, stdout)
	}
	if !strings.HasPrefix(statuses[optionsGlobalRoot], "passed") {
		t.Errorf("infraharvest verify after a change to %s: %s, which it didn't change, didn't pass:\n%s", edited, optionsGlobalRoot, stdout)
	}
}
