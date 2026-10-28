// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"context"
	"errors"
	"reflect"
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
	byTag := &File{Version: Version, Defaults: Defaults{Include: true}, Rules: []Rule{{Exclude: &Match{Tags: map[string]Patterns{"env": {"dev"}}}}}}
	queried := Listed{Resource: harvest.RecordIdentity(map[string]string{"id": "fl-1"}, "fl-1", "aws_flow_log", "aws")}
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
		{"a rule on tags, which a listing by identity doesn't read", byTag, queried, Decision{Reason: report.ReasonSelectionTagsUnread}},
		{"a rule on tags that don't match", byTag, plain, Decision{Include: true}},
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
// include it, by its type or for every type. A selection file also leaves
// out whole the types its defaults exclude, or a rule excludes by type
// alone, but not those it excludes only some resources of.
func TestUnwanted(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	all := Unwanted(cat, nil)
	for _, typ := range cat.SkippedWhole() {
		if !all(typ) {
			t.Errorf("--all: %s is wanted", typ)
		}
	}
	if !all("aws_iam_access_key") || all("aws_sqs_queue") {
		t.Fatalf("--all: access keys unwanted %v, queues %v; want true, false", all("aws_iam_access_key"), all("aws_sqs_queue"))
	}
	excludeAll := Defaults{}
	includeAll := Defaults{Include: true}
	for name, tc := range map[string]struct {
		file     *File
		typ      string
		unwanted bool
	}{
		"an entry that includes one":   {&File{Resources: []Resource{{Type: "aws_iam_access_key", ID: "AKIA1", Include: true}}}, "aws_iam_access_key", false},
		"an entry that excludes one":   {&File{Resources: []Resource{{Type: "aws_iam_access_key", ID: "AKIA1"}}}, "aws_iam_access_key", true},
		"a rule that includes them":    {&File{Rules: []Rule{{Include: &Match{Type: Patterns{"aws_iam_*"}}}}}, "aws_iam_access_key", false},
		"a rule that excludes them":    {&File{Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_iam_*"}}}}}, "aws_iam_access_key", true},
		"a rule for every type":        {&File{Rules: []Rule{{Include: &Match{Tags: map[string]Patterns{"team": {"data"}}}}}}, "aws_iam_access_key", false},
		"a rule for another type":      {&File{Rules: []Rule{{Include: &Match{Type: Patterns{"aws_sqs_*"}}}}}, "aws_iam_access_key", true},
		"defaults that include":        {&File{Defaults: includeAll}, "aws_sqs_queue", false},
		"defaults that exclude":        {&File{Defaults: excludeAll}, "aws_sqs_queue", true},
		"defaults that exclude, entry": {&File{Defaults: excludeAll, Resources: []Resource{{Type: "aws_sqs_queue", ID: "q", Include: true}}}, "aws_sqs_queue", false},
		"defaults that exclude, rule":  {&File{Defaults: excludeAll, Rules: []Rule{{Include: &Match{Tags: map[string]Patterns{"team": {"data"}}}}}}, "aws_sqs_queue", false},
		"an excluding entry":           {&File{Defaults: includeAll, Resources: []Resource{{Type: "aws_sqs_queue", ID: "q"}}}, "aws_sqs_queue", false},
		"a rule excluding the type":    {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_*"}}}}}, "aws_sqs_queue", true},
		"a rule excluding any ID":      {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_queue"}, ID: Patterns{"*"}}}}}, "aws_sqs_queue", true},
		"a rule excluding some IDs":    {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_queue"}, ID: Patterns{"dev-*"}}}}}, "aws_sqs_queue", false},
		"a rule excluding some names":  {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_queue"}, Name: Patterns{"dev"}}}}}, "aws_sqs_queue", false},
		"a rule excluding some tags":   {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_queue"}, Tags: map[string]Patterns{"env": {"dev"}}}}}}, "aws_sqs_queue", false},
		"a rule including some again":  {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_*"}}}, {Include: &Match{Type: Patterns{"aws_sqs_queue"}, ID: Patterns{"prod-*"}}}}}, "aws_sqs_queue", false},
		"a type outside the catalogue": {&File{Defaults: includeAll, Rules: []Rule{{Exclude: &Match{Type: Patterns{"aws_wafv2_web_acl_association"}}}}}, "aws_wafv2_web_acl_association", true},
	} {
		unwanted := Unwanted(cat, tc.file)
		// Asked twice: the second answer is the one worked out first.
		for range 2 {
			if got := unwanted(tc.typ); got != tc.unwanted {
				t.Errorf("%s: %s unwanted %v, want %v", name, tc.typ, got, tc.unwanted)
			}
		}
		// What is unwanted, a file decides out for a resource it doesn't list.
		if tc.unwanted && tc.file.Decide(tc.typ, "other", "other").Include {
			t.Errorf("%s: %s is unwanted, but the file includes a resource of it", name, tc.typ)
		}
	}
}

// A management lock in a resource group another resource manages, such as
// an AKS cluster's node resource group, is left out with the catalogue's
// reason, as what is in the group is; one in a group of the user's is
// kept.
func TestSourcesReasonManagedGroupLock(t *testing.T) {
	t.Parallel()
	const (
		sub = "/subscriptions/00000000-0000-0000-0000-000000000001"
		aks = sub + "/resourceGroups/rg-aks/providers/Microsoft.ContainerService/managedClusters/aks"
	)
	nodes := Listed{Resource: harvest.Record(sub+"/resourceGroups/MC_rg-aks_aks_uksouth/providers/Microsoft.Authorization/locks/nodes", "nodes", "azurerm_management_lock", "azurerm",
		map[string]string{"name": "nodes", "resource_group_name": "MC_rg-aks_aks_uksouth", "resource_group_managed_by": aks})}
	keep := Listed{Resource: harvest.Record(sub+"/resourceGroups/app/providers/Microsoft.Authorization/locks/keep", "keep", "azurerm_management_lock", "azurerm",
		map[string]string{"name": "keep", "resource_group_name": "app"})}
	s, err := NewSources(t.Context(), catalogue.MustBuiltin("azurerm"), nil, []Listed{nodes, keep}, false)
	if err != nil {
		t.Fatal(err)
	}
	const managed = "in a resource group another resource manages, such as an AKS cluster's node resource group (MC_*); that resource creates and deletes what is in it"
	if got := s.Reason(nodes.Resource); got != managed {
		t.Errorf("lock in the node resource group: got %q, want %q", got, managed)
	}
	if got := s.Reason(keep.Resource); got != "" {
		t.Errorf("lock in a group of the user's: got %q, want it kept", got)
	}
}
