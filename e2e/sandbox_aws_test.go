// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || aws)

package e2e

import (
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sandboxAWSServices are the infraharvest services that import what
// testdata/sandbox/aws creates.
var sandboxAWSServices = []string{
	"alb", "api_gateway", "api_gatewayv2", "cloudfront", "cloudwatch", "dynamodb", "ebs",
	"ec2_instance", "ecs", "eip", "iam", "igw", "kinesis", "kms", "lambda", "logs", "nat",
	"route53", "route_table", "s3", "secretsmanager", "sfn", "sg", "sns", "sqs", "ssm",
	"subnet", "vpc", "vpc_endpoint", "wafv2_regional",
}

// sandboxSecondRegion is where testdata/sandbox/aws creates the resources
// of its second region, unless SANDBOX_AWS_SECOND_REGION names another.
const sandboxSecondRegion = "eu-west-1"

// sandboxClusterServices are the infraharvest services that import what
// testdata/sandbox/aws-clusters creates.
var sandboxClusterServices = []string{"eks", "elasticache", "iam", "rds", "sg", "subnet", "vpc"}

// TestSandboxAWS creates testdata/sandbox/aws in a real AWS account as the
// profile SANDBOX_AWS_FIXTURE_PROFILE names, imports its two regions as
// the read-only profile SANDBOX_AWS_READONLY_PROFILE names, as a user
// would, and checks the engine plans every generated resource as an
// import with no changes. On the way it interrupts the import and resumes
// it with both regions at once (see checkResume), and at the end it adds
// a resource to the estate and imports again with --incremental. It
// destroys the estate when it is done. The sandbox-aws workflow runs it,
// after approval; see e2e/README.md.
func TestSandboxAWS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)
	secondRegion := envOr("SANDBOX_AWS_SECOND_REGION", sandboxSecondRegion)
	if secondRegion == s.region {
		t.Fatalf("SANDBOX_AWS_SECOND_REGION is AWS_REGION, %s: the estate needs another region", s.region)
	}

	estate := s.seed(ctx, t, "aws", "suffix="+s.engineName, "second_region="+secondRegion)
	state := estate.apply(ctx, t)

	// Both regions, the second last.
	importSandbox := s.importer(t, []string{s.region, secondRegion}, sandboxAWSServices)
	checkResume(ctx, t, s.out, []string{s.region, secondRegion}, importSandbox)

	coverage, imported := s.checkImport(ctx, t, state)
	checkModuleCalls(t, coverage)
	checkRegions(t, coverage, state, s.region, secondRegion)

	checkSandboxIncremental(ctx, t, s, estate, importSandbox, imported)
}

// TestSandboxAWSClusters creates testdata/sandbox/aws-clusters, a
// PostgreSQL database, a Valkey cache and an EKS control plane, in a real
// AWS account as the fixture profile, imports its region as the read-only
// profile, and checks the engine plans every generated resource as an
// import with no changes, and leaves out the security group EKS creates
// for the cluster. These take minutes to create and delete and cost by
// the hour, so the sandbox-aws workflow runs this test only when asked to
// (estate=clusters); see e2e/README.md.
func TestSandboxAWSClusters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)

	estate := s.seed(ctx, t, "aws-clusters", "suffix="+s.engineName)
	state := estate.apply(ctx, t)

	s.importer(t, []string{s.region}, sandboxClusterServices).must(ctx, t)
	coverage, _ := s.checkImport(ctx, t, state)
	checkEKSClusterGroup(t, coverage, state)
}

// sandboxDataServices are the infraharvest services that import what
// testdata/sandbox/aws-data creates.
var sandboxDataServices = []string{"ec2_instance", "iam", "lambda", "logs", "rds", "redshift", "s3", "sg", "subnet", "vpc"}

// TestSandboxAWSData creates testdata/sandbox/aws-data, an EC2 instance,
// a Lambda function, a PostgreSQL database, a Redshift cluster and an S3
// bucket, in a real AWS account as the fixture profile, imports its region
// as the read-only profile, and checks the engine plans every generated
// resource as an import with no changes. The database and the cluster
// cost by the hour, so the sandbox-aws workflow runs this test only when
// asked to (estate=data), and destroys the estate straight after; see
// e2e/README.md.
func TestSandboxAWSData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)

	estate := s.seed(ctx, t, "aws-data", "suffix="+s.engineName)
	state := estate.apply(ctx, t)

	s.importer(t, []string{s.region}, sandboxDataServices).must(ctx, t)
	s.checkImport(ctx, t, state)
}

// sandbox is what a sandbox test runs with: the account's profiles and
// region, the engine, and the directories that outlive the test, so that
// the workflow can destroy what a failed run left and upload the import.
type sandbox struct {
	fixtureProfile, readonlyProfile, region string
	engineName, execPath                    string
	fixtureDir, plugins, out                string
}

// newSandbox returns what the test runs with, from the environment the
// sandbox-aws workflow sets. It skips the test if no account is named.
func newSandbox(ctx context.Context, t *testing.T) *sandbox {
	t.Helper()
	s := &sandbox{
		fixtureProfile:  os.Getenv("SANDBOX_AWS_FIXTURE_PROFILE"),
		readonlyProfile: os.Getenv("SANDBOX_AWS_READONLY_PROFILE"),
		region:          os.Getenv("AWS_REGION"),
	}
	if s.fixtureProfile == "" || s.readonlyProfile == "" || s.region == "" {
		skipUnconfigured(t, "SANDBOX_AWS_FIXTURE_PROFILE, SANDBOX_AWS_READONLY_PROFILE and AWS_REGION name a sandbox account; see the sandbox-aws workflow")
	}
	if os.Getenv("AWS_ENDPOINT_URL") != "" {
		t.Fatal("AWS_ENDPOINT_URL is set: the sandbox test runs against AWS, not an emulator")
	}
	onEmulator = false
	s.fixtureDir = envOr("SANDBOX_FIXTURE_DIR", t.TempDir())
	s.plugins = envOr("SANDBOX_PLUGIN_CACHE", t.TempDir())
	s.out = envOr("SANDBOX_OUTPUT_DIR", t.TempDir())
	var binary engine.Binary
	s.engineName, binary = e2eEngine(t)
	var err error
	if s.execPath, err = binary.Find(ctx, "", filepath.Join(s.plugins, binary.Name)); err != nil {
		t.Fatal(err)
	}
	return s
}

// seed writes the estate testdata/sandbox/<name> into the fixture
// directory, to apply as the fixture profile with vars (see seedSandbox).
func (s *sandbox) seed(ctx context.Context, t *testing.T, name string, vars ...string) *sandboxEstate {
	t.Helper()
	return seedSandbox(ctx, t, name, s.fixtureDir, s.execPath, s.plugins, map[string]string{"AWS_PROFILE": s.fixtureProfile}, vars...)
}

// importer returns the import of services in regions, as the read-only
// profile, into the output directory. Call it once the estate is applied:
// from then on the test's own AWS calls are read-only too.
func (s *sandbox) importer(t *testing.T, regions, services []string) sandboxImport {
	t.Helper()
	t.Setenv("AWS_PROFILE", s.readonlyProfile)
	return func(ctx context.Context, args ...string) error {
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "aws",
			"--engine=" + s.engineName,
			"--terraform-path=" + s.execPath,
			"--profile=" + s.readonlyProfile,
			"--regions=" + strings.Join(regions, ","),
			"--services=" + strings.Join(services, ","),
			"--out=" + s.out,
			"--all",
			"--allow-partial",
		}, args...))
		return root.ExecuteContext(ctx)
	}
}

// checkImport checks the import in the output directory: nothing failed
// or was left out, every check passed, every root plans as imports with
// no changes, and each type the estate created is imported at least as
// many times as it was created. It returns the import's report and how
// many resources of each type the roots import.
func (s *sandbox) checkImport(ctx context.Context, t *testing.T, created []*tfjson.StateResource) (report.Report, map[string]int) {
	t.Helper()
	coverage := readCoverage(t, s.out)
	t.Logf("totals: %+v", coverage.Totals)
	for _, f := range coverage.Failures {
		t.Errorf("failure: %+v", f)
	}
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("%s: %s (%s) left out: %v", d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	imported := map[string]int{}
	for _, dir := range generatedDirs(t, s.out) {
		if rejected, err := os.ReadFile(filepath.Join(dir, engine.RejectedFileName)); err == nil {
			t.Errorf("%s: resources left out:\n%s", dir, rejected)
		}
		for typ, n := range checkNoChanges(ctx, t, dir, s.execPath, s.plugins, secretValues(t, dir, created)) {
			imported[typ] += n
		}
	}
	checkApplied(ctx, t, s.out, generatedDirs(t, s.out), s.execPath, s.plugins, map[string]string{"AWS_PROFILE": s.fixtureProfile}, func(dir string) []tfexec.PlanOption { return secretValues(t, dir, created) })
	seeded := map[string]int{}
	for _, r := range created {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
	return coverage, imported
}

// checkEKSClusterGroup checks that the import leaves out the security
// group EKS created for the estate's cluster, as EKS's own.
func checkEKSClusterGroup(t *testing.T, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	group := ""
	for _, r := range created {
		if r.Type != "aws_eks_cluster" {
			continue
		}
		if configs, ok := r.AttributeValues["vpc_config"].([]any); ok && len(configs) == 1 {
			if config, ok := configs[0].(map[string]any); ok {
				group, _ = config["cluster_security_group_id"].(string)
			}
		}
	}
	if group == "" {
		t.Fatal("the estate's cluster has no cluster security group")
	}
	for _, e := range coverage.Excluded {
		if e.Type == "aws_security_group" && e.ID == group {
			t.Logf("%s left out: %s", group, e.Reason)
			return
		}
	}
	t.Errorf("the import doesn't leave out %s, the security group EKS created for the cluster", group)
}

// sandboxImport runs infraharvest import aws on the sandbox estate, with
// args besides the flags every import of it takes.
type sandboxImport func(ctx context.Context, args ...string) error

// must runs the import and fails the test if it couldn't run.
func (run sandboxImport) must(ctx context.Context, t *testing.T, args ...string) {
	t.Helper()
	if err := run(ctx, args...); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import aws %v: %v", args, err)
	}
}

// checkResume imports the sandbox's regions into out, one at a time,
// cancelling the import as Ctrl-C would when it starts generating the root
// of the last of regions, then imports again with --resume and the
// regions at once (--parallel): they list at the same time, the roots
// generated before the interrupt stay as they were, and the interrupted
// root is generated as an import that wasn't interrupted generates it,
// which the test checks by generating it once more, from nothing, one
// region at a time.
func checkResume(ctx context.Context, t *testing.T, out string, regions []string, run sandboxImport) {
	t.Helper()
	lastRegion := regions[len(regions)-1]
	interruptCtx, interruptRun := context.WithCancel(ctx)
	defer interruptRun()
	interrupt := &interrupter{region: lastRegion, cancel: interruptRun}
	logs := log.Writer()
	log.SetOutput(io.MultiWriter(logs, interrupt))
	err := run(interruptCtx)
	log.SetOutput(logs)
	dir := interrupt.result()
	switch {
	case dir == "":
		t.Fatalf("the import didn't start on the root of %s: %v", lastRegion, err)
	case !errors.Is(err, context.Canceled):
		t.Fatalf("the import went on after the interrupt: %v", err)
	}
	rel, err := filepath.Rel(out, dir)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)

	// kept returns the files of the roots generated before the interrupt,
	// and when their imports.tf was written.
	kept := func() (map[string]string, map[string]int64) {
		files := map[string]string{}
		for name, content := range outputFiles(t, out) {
			if !strings.HasPrefix(name, rel+"/") && !strings.HasPrefix(name, report.Dir+"/") {
				files[name] = content
			}
		}
		times := generatedTimes(t, out)
		delete(times, dir)
		return files, times
	}
	files, times := kept()
	if len(times) == 0 {
		t.Fatal("the import generated no root before the interrupt")
	}
	checkKept := func(when string) {
		t.Helper()
		gotFiles, gotTimes := kept()
		if !reflect.DeepEqual(gotTimes, times) {
			t.Errorf("%s generated roots again: %v, then %v", when, times, gotTimes)
		}
		for name, content := range files {
			if gotFiles[name] != content {
				t.Errorf("%s changed %s", when, name)
			}
		}
		for name := range gotFiles {
			if _, ok := files[name]; !ok {
				t.Errorf("%s wrote %s", when, name)
			}
		}
	}

	// The regions at once: the roots they share with the import above, and
	// the one generated again below, show the output doesn't depend on it.
	resumeAtOnce := func() *stepLog {
		logged := &stepLog{}
		writer := log.Writer()
		log.SetOutput(io.MultiWriter(writer, logged))
		defer log.SetOutput(writer)
		run.must(ctx, t, "--resume", "--parallel="+strconv.Itoa(len(regions)))
		return logged
	}
	// Each region lists the whole of its services from AWS, which takes
	// long enough for them to overlap; the roots that resume generate in
	// no time, so they may not.
	first, last := "region "+regions[0], "region "+lastRegion
	if steps := resumeAtOnce().overlapping(t, first, last); len(steps) == 0 {
		t.Errorf("with --parallel=%d, %s and %s ran one after the other", len(regions), first, last)
	} else {
		t.Logf("with --parallel=%d, %s and %s ran at the same time: %v", len(regions), first, last, steps)
	}
	checkKept("--resume after the interrupt")
	resumed := rootFiles(t, out, rel)
	if resumed[rel+"/"+engine.ImportsFileName] == "" {
		t.Fatalf("--resume didn't generate %s", dir)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	run.must(ctx, t, "--resume")
	checkKept("--resume")
	again := rootFiles(t, out, rel)
	for name, content := range resumed {
		if again[name] != content {
			t.Errorf("%s differs between the import resumed after the interrupt and one that wasn't interrupted", name)
		}
	}
	for name := range again {
		if _, ok := resumed[name]; !ok {
			t.Errorf("the import that wasn't interrupted wrote %s, the resumed one didn't", name)
		}
	}
}

// interrupter is a log writer that cancels the import, as Ctrl-C does,
// when the import logs that it starts generating the root of region.
type interrupter struct {
	region string
	cancel context.CancelFunc
	mu     sync.Mutex
	dir    string // the root it interrupted the import on
}

func (w *interrupter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	_, dir, ok := strings.Cut(message, " resources in ")
	if !ok || !strings.Contains(message, "generating configuration for ") || filepath.Base(dir) != w.region {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dir != "" {
		return len(p), nil
	}
	w.dir = filepath.Clean(dir)
	w.cancel()
	return len(p), nil
}

// result returns the root the import was interrupted on, "" if it wasn't.
func (w *interrupter) result() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dir
}

// rootFiles returns the files outputFiles returns of the root at rel
// under out.
func rootFiles(t *testing.T, out, rel string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for name, content := range outputFiles(t, out) {
		if strings.HasPrefix(name, rel+"/") {
			files[name] = content
		}
	}
	return files
}

// checkModuleCalls checks that both VPCs, with their NAT gateways, become
// calls of the vpc module, one of them with a NAT gateway in each zone,
// and the bucket a call of the s3-bucket module.
func checkModuleCalls(t *testing.T, coverage report.Report) {
	t.Helper()
	calls := map[string]int{}
	perZone := false
	for _, d := range coverage.Directories {
		for _, m := range d.Modules {
			t.Logf("%s: module %s %s: %v declined=%q", d.Path, m.Source, m.Version, m.Resources, m.Declined)
			if m.Declined != "" {
				continue
			}
			calls[m.Source]++
			nats := 0
			for _, r := range m.Resources {
				if strings.HasPrefix(r, "aws_nat_gateway.") {
					nats++
				}
			}
			perZone = perZone || (m.Source == adapters.VPC.Source && nats > 1)
		}
	}
	for source, want := range map[string]int{adapters.VPC.Source: 2, adapters.S3Bucket.Source: 1} {
		if calls[source] < want {
			t.Errorf("%d calls of %s, want %d or more", calls[source], source, want)
		}
	}
	if !perZone {
		t.Errorf("no call of %s with a NAT gateway in each zone", adapters.VPC.Source)
	}
}

// sandboxPlacedTypes are the types of the estate checkRegions finds the
// roots of, by whether they are global: their import IDs are their IDs.
var sandboxPlacedTypes = map[string]bool{
	"aws_sqs_queue":               false,
	"aws_ssm_parameter":           false,
	"aws_iam_role":                true,
	"aws_route53_zone":            true,
	"aws_cloudfront_distribution": true,
}

// checkRegions checks that the import put each region's resources in the
// root of that region, and the global ones, such as IAM roles, Route 53
// zones and CloudFront distributions, in the global root only, once.
func checkRegions(t *testing.T, coverage report.Report, created []*tfjson.StateResource, regions ...string) {
	t.Helper()
	scopes := append([]string{"global"}, regions...)
	// The roots, by the region they are named after, that import each
	// resource, by type and ID.
	roots := map[string][]string{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			typ, _, _ := strings.Cut(r.Address, ".")
			roots[typ+" "+r.ID] = append(roots[typ+" "+r.ID], path.Base(d.Path))
		}
	}
	placed := map[string]bool{}
	for _, r := range created {
		global, ok := sandboxPlacedTypes[r.Type]
		if !ok {
			continue
		}
		id, _ := r.AttributeValues["id"].(string)
		want := "global"
		if !global {
			// Every regional resource has its region, with version 6 of the
			// provider.
			want, _ = r.AttributeValues["region"].(string)
		}
		placed[want] = true
		if got := roots[r.Type+" "+id]; !slices.Equal(got, []string{want}) {
			t.Errorf("%s %s is in the roots of %v, want %s's only", r.Type, id, got, want)
		}
	}
	for _, scope := range scopes {
		if !placed[scope] {
			t.Errorf("none of the estate's %v is in %s", slices.Sorted(maps.Keys(sandboxPlacedTypes)), scope)
		}
	}
}

// checkSandboxIncremental creates the estate's extra log group, then
// imports into out again with --incremental: the import adds the log
// group to the root of region, in a file of its own, and leaves the rest
// as it was. Every root still plans with no changes, importing what
// imported counts and the log group, nothing twice.
func checkSandboxIncremental(ctx context.Context, t *testing.T, s *sandbox, estate *sandboxEstate, run sandboxImport, imported map[string]int) {
	t.Helper()
	out, region := s.out, s.region
	before := outputFiles(t, out)
	// Only the log group: the rest stays as the import found it.
	created := estate.apply(ctx, t, tfexec.Var("extra=true"), tfexec.Target("aws_cloudwatch_log_group.extra"))
	logGroup := ""
	for _, r := range created {
		if r.Type == "aws_cloudwatch_log_group" && r.Name == "extra" {
			logGroup, _ = r.AttributeValues["id"].(string)
		}
	}
	if logGroup == "" {
		t.Fatal("the estate has no extra log group")
	}

	run.must(ctx, t, "--incremental")
	after := outputFiles(t, out)
	// The log group goes after the blocks of its type's file, or into a
	// new one; the root's blocks stay as they were.
	added := addedConfiguration(t, before, after)
	if len(added) != 1 {
		t.Fatalf("--incremental added to %v, want to one file of the root of %s", slices.Sorted(maps.Keys(added)), region)
	}
	var file string
	for name := range added {
		file = name
	}
	root := path.Dir(file)
	if path.Base(root) != region {
		t.Fatalf("--incremental added to %s, want to the root of %s", file, region)
	}
	if blocks := topBlocks(t, file, added[file]); len(blocks) != 1 || blocks[0].Type != "resource" || blocks[0].Labels[0] != "aws_cloudwatch_log_group" {
		t.Errorf("--incremental added more than the log group to %s:\n%s", file, added[file])
	}
	// The import blocks, data sources, variables and locals of the root may
	// grow; nothing else changes.
	for name, content := range before {
		if slices.Contains(growingFiles, path.Base(name)) && path.Dir(name) != root && after[name] != content {
			t.Errorf("--incremental changed %s", name)
		}
	}
	imports := root + "/" + engine.ImportsFileName
	if n, m := importBlocks(t, imports, before[imports]), importBlocks(t, imports, after[imports]); m != n+1 {
		t.Errorf("%s has %d import blocks after --incremental, %d before", imports, m, n)
	}

	coverage := readCoverage(t, out)
	inRoot := 0
	for _, e := range coverage.Excluded {
		if e.Reason == importer.InRootReason {
			inRoot++
		}
	}
	if inRoot == 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json of --incremental: %d in the roots already, failures %v", inRoot, coverage.Failures)
	}
	if len(coverage.Directories) != 1 || coverage.Directories[0].Path != root ||
		len(coverage.Directories[0].Imported) != 1 || coverage.Directories[0].Imported[0].ID != logGroup {
		t.Errorf("coverage.json of --incremental reports %+v, want %s added to %s", coverage.Directories, logGroup, root)
	}
	for _, d := range coverage.Directories {
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("--incremental: %s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	want := maps.Clone(imported)
	want["aws_cloudwatch_log_group"]++
	got := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(ctx, t, dir, s.execPath, s.plugins, secretValues(t, dir, created)) {
			got[typ] += n
		}
	}
	types := maps.Clone(want)
	maps.Copy(types, got)
	for _, typ := range sortedKeys(types) {
		if got[typ] != want[typ] {
			t.Errorf("%s: the roots import %d after --incremental, want %d", typ, got[typ], want[typ])
		}
	}
}

// topBlocks returns the top-level blocks of a configuration file.
func topBlocks(t *testing.T, name, content string) []*hclsyntax.Block {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(content), name, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return f.Body.(*hclsyntax.Body).Blocks
}

// importBlocks counts the import blocks of an imports.tf.
func importBlocks(t *testing.T, name, content string) int {
	t.Helper()
	n := 0
	for _, b := range topBlocks(t, name, content) {
		if b.Type == "import" {
			n++
		}
	}
	return n
}
