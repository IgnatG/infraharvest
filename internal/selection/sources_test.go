// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// providerDefaults are a provider's own defaults: they leave out what
// excluded names, by "<type> <id>", fail with err, and keep what they were
// asked about.
type providerDefaults struct {
	excluded map[string]string
	err      error
	asked    []harvest.Resource
}

func (p *providerDefaults) ExcludedByDefault(_ context.Context, resources []harvest.Resource) (map[string]string, error) {
	p.asked = resources
	return p.excluded, p.err
}

// The listed resources of the precedence tests, by name, with their tags.
var (
	plain       = Listed{Resource: harvest.RecordBare("plain", "plain", "aws_s3_bucket", "aws")}
	serviceRole = Listed{Resource: harvest.RecordBare("AWSServiceRoleForECS", "AWSServiceRoleForECS", "aws_iam_role", "aws")}
	defaultVPC  = Listed{Resource: harvest.RecordBare("vpc-default", "vpc-default", "aws_vpc", "aws")}
	inState     = Listed{Resource: harvest.RecordBare("state", "state", "aws_s3_bucket", "aws")}
	tagged      = Listed{Resource: harvest.RecordBare("tagged", "tagged", "aws_s3_bucket", "aws"), Tags: map[string]string{"ManagedBy": "Terraform"}}
	stacked     = Listed{Resource: harvest.RecordBare("stacked", "stacked", "aws_s3_bucket", "aws"), Tags: map[string]string{"ManagedBy": "Terraform"}}
	inside      = Listed{Resource: harvest.RecordBare("inside", "inside", "aws_s3_bucket", "aws"), Tags: map[string]string{"aws:cloudformation:stack-name": "app"}}
	insideState = Listed{Resource: harvest.RecordBare("inside-state", "inside-state", "aws_s3_bucket", "aws")}
	outside     = Listed{Resource: harvest.RecordBare("outside", "outside", "aws_s3_bucket", "aws")}
)

const (
	stackReason   = "managed by CloudFormation stack app"
	outsideReason = report.OutsideScopePrefix + "not a resource of CloudFormation stack app (--cfn-stack)"
)

// testSources returns the sources of the default selection of the
// precedence tests' resources: the catalogue skips the service role; the
// provider leaves out the default VPC, and the service role, the bucket in
// a state and the tagged stack's bucket too; a state has two buckets; and
// an import scope takes two buckets and leaves out another.
func testSources(t *testing.T, includeOwned bool) *Sources {
	t.Helper()
	provider := &providerDefaults{excluded: map[string]string{
		"aws_iam_role AWSServiceRoleForECS": "the provider's reason",
		"aws_vpc vpc-default":               "default VPC",
		"aws_s3_bucket state":               "the provider's reason",
		"aws_s3_bucket stacked":             stackReason,
	}}
	listed := []Listed{plain, serviceRole, defaultVPC, inState, tagged, stacked, inside, insideState, outside}
	s, err := NewSources(t.Context(), catalogue.MustBuiltin("aws"), provider, listed, includeOwned)
	if err != nil {
		t.Fatal(err)
	}
	s.Managed(inState.Resource, "prod.tfstate")
	s.Managed(insideState.Resource, "prod.tfstate")
	s.Managed(outside.Resource, "prod.tfstate")
	s.Scope(&harvest.ImportScope{
		Inside:  map[string]bool{"aws_s3_bucket inside": true, "aws_s3_bucket inside-state": true},
		Outside: map[string]string{"aws_s3_bucket outside": outsideReason},
	})
	return s
}

// The sources decide why a resource is left out in one order: the import
// scope's outside, a state, the catalogue, the provider, then the tags,
// with what CloudFormation manages taken inside the scope.
func TestSourcesReason(t *testing.T) {
	t.Parallel()
	skipped := catalogue.MustBuiltin("aws").Skipped("aws_iam_role", "AWSServiceRoleForECS", nil)
	if skipped == "" {
		t.Fatal("the catalogue no longer skips AWSServiceRoleForECS")
	}
	inProd := report.TerraformPrefix + " (prod.tfstate)"
	byTag := report.TerraformPrefix + " (Terraform elsewhere, per its tag ManagedBy=Terraform; --include-owned imports it)"
	for _, tc := range []struct {
		name         string
		listed       Listed
		want         string
		includeOwned string
	}{
		{"nothing leaves it out", plain, "", ""},
		{"the catalogue before the provider", serviceRole, skipped, skipped},
		{"the provider", defaultVPC, "default VPC", "default VPC"},
		{"a state before the provider", inState, inProd, inProd},
		{"the tags, unless --include-owned", tagged, byTag, ""},
		{"the provider before the tags", stacked, stackReason, stackReason},
		{"CloudFormation's inside the scope", inside, "", ""},
		{"a state's inside the scope", insideState, inProd, inProd},
		{"outside the scope before a state", outside, outsideReason, outsideReason},
	} {
		if got := testSources(t, false).Reason(tc.listed.Resource); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if got := testSources(t, true).Reason(tc.listed.Resource); got != tc.includeOwned {
			t.Errorf("%s, with --include-owned: got %q, want %q", tc.name, got, tc.includeOwned)
		}
	}
}

// Without a selection file the defaults decide. With one, its entry
// decides, then its rules, except that a rule doesn't include what another
// state or tool manages, then the defaults, then its defaults; what is
// outside the import scope stays out whatever the file says.
func TestSourcesDecide(t *testing.T) {
	t.Parallel()
	s := testSources(t, false)
	includeAll := &File{Version: Version, Rules: []Rule{{Include: &Match{Type: Patterns{"*"}}}}}
	entries := &File{Version: Version, Resources: []Resource{
		{Type: "aws_s3_bucket", ID: "state", Include: true},
		{Type: "aws_s3_bucket", ID: "outside", Include: true},
		{Type: "aws_s3_bucket", ID: "plain"},
	}}
	excludeByDefault := &File{Version: Version}
	inProd := report.TerraformPrefix + " (prod.tfstate)"
	for _, tc := range []struct {
		name   string
		file   *File
		listed Listed
		want   Decision
	}{
		{"--all, nothing leaves it out", nil, plain, Decision{Include: true}},
		{"--all, the defaults", nil, defaultVPC, Decision{Reason: "default VPC"}},
		{"a rule, over the cloud's default", includeAll, defaultVPC, Decision{Include: true}},
		{"a rule, not over a state", includeAll, inState, Decision{Reason: inProd}},
		{"a rule, not over the tags", includeAll, tagged, Decision{Reason: OwnedReason(mustOwner(t, tagged.Tags))}},
		{"a rule, not over a stack", includeAll, stacked, Decision{Reason: stackReason}},
		{"a rule, not outside the scope", includeAll, outside, Decision{Reason: outsideReason}},
		{"an entry, over a state", entries, inState, Decision{Include: true}},
		{"an entry, not outside the scope", entries, outside, Decision{Reason: outsideReason}},
		{"an entry that excludes", entries, plain, Decision{Reason: report.ReasonSelectionEntry}},
		{"the defaults before the file's", excludeByDefault, defaultVPC, Decision{Reason: "default VPC"}},
		{"the file's defaults", excludeByDefault, plain, Decision{Reason: report.ReasonSelectionDefaults}},
	} {
		if got := s.Decide(tc.file, "", tc.listed.Observed.ID, tc.listed); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// mustOwner returns what tags say manages a resource.
func mustOwner(t *testing.T, tags map[string]string) engine.Owner {
	t.Helper()
	owner, ok := engine.OwnedBy(tags)
	if !ok {
		t.Fatalf("tags %v name no owner", tags)
	}
	return owner
}

// The provider sees the listed tags among the attributes, as tags.<key>,
// in a copy that leaves the lister's record as it was; a tag the lister
// recorded itself wins over the listed one. What it found before it
// failed counts, with its error.
func TestNewSourcesAsksTheProvider(t *testing.T) {
	t.Parallel()
	r := harvest.Record("id-1", "one", "fake_thing", "fake", map[string]string{"name": "one", "tags.team": "listed by the lister"})
	untagged := harvest.RecordBare("id-2", "two", "fake_thing", "fake")
	failed := errors.New("access denied")
	provider := &providerDefaults{excluded: map[string]string{"fake_thing id-2": "found before it failed"}, err: failed}

	s, err := NewSources(t.Context(), &catalogue.Catalogue{}, provider, []Listed{
		{Resource: r, Tags: map[string]string{"team": "data", "env": "dev"}},
		{Resource: untagged},
	}, false)

	if !errors.Is(err, failed) {
		t.Errorf("error %v, want the provider's", err)
	}
	if got := s.Reason(untagged); got != "found before it failed" {
		t.Errorf("reason %q", got)
	}
	want := map[string]string{"name": "one", "tags.team": "listed by the lister", "tags.env": "dev"}
	if len(provider.asked) != 2 || !reflect.DeepEqual(provider.asked[0].Observed.Attributes, want) {
		t.Errorf("asked about %+v, want attributes %v", provider.asked, want)
	}
	if len(r.Observed.Attributes) != 2 {
		t.Errorf("the lister's record changed: %v", r.Observed.Attributes)
	}
	if provider.asked[1].Observed != untagged.Observed {
		t.Error("without tags, want the resource as it is")
	}
}

// Resources another state or tool manages are left out with the reason,
// which the report counts as managed by Terraform or by another tool.
func TestOwnedReason(t *testing.T) {
	t.Parallel()
	pulumi := harvest.Record("web", "web", "google_storage_bucket", "google", map[string]string{"labels.managed-by": "pulumi"})
	want := map[string]string{
		"already managed by Terraform (Terraform elsewhere, per its tag ManagedBy=Terraform; --include-owned imports it)": "ManagedBy=Terraform",
		"already managed by Terraform (OpenTofu elsewhere, per its tag managed-by=opentofu; --include-owned imports it)":  "managed-by=opentofu",
		"managed by CloudFormation stack app (per its tag aws:cloudformation:stack-name=app; --include-owned imports it)": "aws:cloudformation:stack-name=app",
		"managed by Pulumi (per its tag managed-by=pulumi; --include-owned imports it)":                                   "managed-by=pulumi",
	}
	r := &report.Report{}
	for _, tags := range []map[string]string{
		{"ManagedBy": "Terraform"},
		{"managed-by": "opentofu"},
		{"aws:cloudformation:stack-name": "app"},
		harvest.AttributeTags(pulumi.Observed.Attributes),
	} {
		reason := OwnedReason(mustOwner(t, tags))
		if _, ok := want[reason]; !ok {
			t.Errorf("tags %v: reason %q", tags, reason)
		}
		r.Excluded = append(r.Excluded, report.Excluded{Type: "t", ID: reason, Reason: reason})
	}
	r.Finish(nil, nil, false)
	if r.Totals.Managed != 2 || r.Totals.OtherTool != 2 {
		t.Errorf("totals: got %+v, want 2 managed and 2 by another tool", r.Totals)
	}
}

// With --all, the types the default selection leaves out whole are
// unwanted; a selection file wants one again if an entry or a rule may
// include it, by its type or for every type.
func TestUnwanted(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	if got := Unwanted(cat, nil); !reflect.DeepEqual(got, cat.SkippedWhole()) || !slices.Contains(got, "aws_iam_access_key") {
		t.Fatalf("--all: got %v, want %v", got, cat.SkippedWhole())
	}
	for name, tc := range map[string]struct {
		file     *File
		unwanted bool
	}{
		"an entry that includes one": {&File{Resources: []Resource{{Type: "aws_iam_access_key", ID: "AKIA1", Include: true}}}, false},
		"an entry that excludes one": {&File{Resources: []Resource{{Type: "aws_iam_access_key", ID: "AKIA1"}}}, true},
		"a rule that includes them":  {&File{Rules: []Rule{{Include: &Match{Type: Patterns{"aws_iam_*"}}}}}, false},
		"a rule that excludes them":  {&File{Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_iam_*"}}}}}, true},
		"a rule for every type":      {&File{Rules: []Rule{{Include: &Match{Tags: map[string]Patterns{"team": {"data"}}}}}}, false},
		"a rule for another type":    {&File{Rules: []Rule{{Include: &Match{Type: Patterns{"aws_sqs_*"}}}}}, true},
	} {
		if got := slices.Contains(Unwanted(cat, tc.file), "aws_iam_access_key"); got != tc.unwanted {
			t.Errorf("%s: access keys unwanted %v, want %v", name, got, tc.unwanted)
		}
	}
}
