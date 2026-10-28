// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

const explorerViewARN = "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/default/1111"

// explorerTestNow is the time in the tests, well after the default index
// was created.
var explorerTestNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func explorerIndex(typ, createdAt, extra string) string {
	return fmt.Sprintf(`{"Arn":"arn:aws:resource-explorer-2:eu-west-2:123456789012:index/1","State":"ACTIVE","Type":%q,"CreatedAt":%q%s}`, typ, createdAt, extra)
}

// fakeExplorer answers Resource Explorer's calls: by default, an active
// local index created months ago, an unfiltered default view of the
// account, and a search of the region that finds one resource of each of
// present, one a page. Every other call is recorded and answered with no
// resources.
type fakeExplorer struct {
	// indexes and views answer GetIndex and GetDefaultView in turn, the
	// last one from then on.
	indexes, views []string
	view           string
	supported      []string
	present        []string
	// more are types the region also has that a search of the region
	// doesn't return, as when it has more than a search returns.
	more     []string
	complete bool
	// errs answers the calls to these paths with these errors.
	errs    map[string]string
	gets    map[string]int
	queries []string
	calls   []string
}

func newFakeExplorer() *fakeExplorer {
	return &fakeExplorer{
		indexes:   []string{explorerIndex("LOCAL", "2026-01-01T00:00:00Z", "")},
		views:     []string{`{"ViewArn":"` + explorerViewARN + `"}`},
		view:      `{"View":{"ViewArn":"` + explorerViewARN + `","Scope":"arn:aws:iam::123456789012:root","Filters":{"FilterString":""}}}`,
		supported: []string{"ec2:instance", "states:activity", "states:stateMachine", "sqs:queue"},
		present:   []string{"ec2:instance", "states:stateMachine"},
		complete:  true,
		errs:      map[string]string{},
		gets:      map[string]int{},
	}
}

func (f *fakeExplorer) handle(call apiCall) string {
	if e, ok := f.errs[call.Path]; ok {
		return e
	}
	inTurn := func(answers []string) string {
		n := f.gets[call.Path]
		f.gets[call.Path]++
		return answers[min(n, len(answers)-1)]
	}
	switch call.Path {
	case "/GetIndex":
		return inTurn(f.indexes)
	case "/GetDefaultView":
		return inTurn(f.views)
	case "/GetView":
		return f.view
	case "/ListSupportedResourceTypes":
		// Two pages.
		half := len(f.supported) / 2
		types, token := f.supported[:half], nextPageToken
		if isSecondPage(call) {
			types, token = f.supported[half:], ""
		}
		return fmt.Sprintf(`{"ResourceTypes":[%s],"NextToken":%q}`, resourceTypesJSON(types), token)
	case "/Search":
		return f.search(call)
	}
	f.calls = append(f.calls, call.Op)
	if call.Op == "ListQueues" {
		return `{"QueueUrls":["https://sqs.eu-west-2.amazonaws.com/123456789012/orders"]}`
	}
	return `{}`
}

func (f *fakeExplorer) search(call apiCall) string {
	var in struct{ QueryString, NextToken, ViewArn string }
	if err := json.Unmarshal([]byte(call.Body), &in); err != nil {
		return `{"__type":"ValidationException","message":"bad body"}`
	}
	if in.ViewArn != explorerViewARN {
		return `{"__type":"ValidationException","message":"unexpected view"}`
	}
	f.queries = append(f.queries, in.QueryString)
	if _, list, ok := strings.Cut(in.QueryString, " resourcetype:"); ok {
		// A search for some types: one resource if the region has one.
		for _, t := range strings.Split(list, ",") {
			if slices.Contains(f.present, t) || slices.Contains(f.more, t) {
				return fmt.Sprintf(`{"Resources":[%s],"Count":{"Complete":true,"TotalResources":1}}`, resourceJSON(t))
			}
		}
		return `{"Resources":[],"Count":{"Complete":true,"TotalResources":0}}`
	}
	// The token is the next page's number.
	page, _ := strconv.Atoi(in.NextToken)
	var resources, token string
	if page < len(f.present) {
		resources = resourceJSON(f.present[page])
	}
	if page+1 < len(f.present) {
		token = strconv.Itoa(page + 1)
	}
	return fmt.Sprintf(`{"Resources":[%s],"NextToken":%q,"Count":{"Complete":%t,"TotalResources":%d}}`, resources, token, f.complete, len(f.present))
}

func resourceTypesJSON(types []string) string {
	var items []string
	for _, t := range types {
		service, _, _ := strings.Cut(t, ":")
		items = append(items, fmt.Sprintf(`{"ResourceType":%q,"Service":%q}`, t, service))
	}
	return strings.Join(items, ",")
}

func resourceJSON(typ string) string {
	service, resource, _ := strings.Cut(typ, ":")
	return fmt.Sprintf(`{"Arn":"arn:aws:%s:eu-west-2:123456789012:%s/x","Region":"eu-west-2","ResourceType":%q,"Service":%q}`, service, resource, typ, service)
}

// explorerRun returns the context of a run of its own that lists against
// f, at explorerTestNow.
func explorerRun(t *testing.T, f *fakeExplorer) context.Context {
	t.Helper()
	ctx := useFakeAPI(t, f.handle)
	configsOf(ratelimit.FromContext(ctx)).explorerNow = func() time.Time { return explorerTestNow }
	return ctx
}

// useExplorer returns the context of a run of its own that lists against
// f, and what it logs: the log is the process's, so the tests that read
// it don't run in parallel.
func useExplorer(t *testing.T, f *fakeExplorer) (context.Context, *bytes.Buffer) {
	t.Helper()
	ctx := explorerRun(t, f)
	logs := &bytes.Buffer{}
	writer := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(writer) })
	return ctx, logs
}

// listRegion lists service in eu-west-2 as an import does: through the
// guardedLister of a provider that uses Resource Explorer if explorer
// says so, in the run of ctx.
func listRegion(ctx context.Context, t *testing.T, service string, explorer bool) []harvest.Resource {
	t.Helper()
	p := &Provider{}
	if explorer {
		p.UseResourceExplorer(true)
	}
	if err := p.Init(ctx, []string{"eu-west-2", "default"}); err != nil {
		t.Fatal(err)
	}
	lister, err := p.Lister(service)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := lister.List(ctx, harvest.Settings{Service: service, Provider: "aws"})
	if err != nil {
		t.Fatal(err)
	}
	return resources
}

func TestResourceExplorerSkipsServicesTheRegionHasNoneOf(t *testing.T) {
	f := newFakeExplorer()
	ctx, logs := useExplorer(t, f)

	sqs := listRegion(ctx, t, "sqs", true)
	listRegion(ctx, t, "sfn", true)

	// sqs:queue is on the second page of supported types, and the region
	// has none.
	if len(sqs) != 0 || slices.Contains(f.calls, "ListQueues") {
		t.Errorf("sqs was listed: %v, resources %v", f.calls, resourceIDs(sqs, "aws_sqs_queue"))
	}
	// The region has a state machine, on the second page of the search.
	if !slices.Contains(f.calls, "ListStateMachines") || !slices.Contains(f.calls, "ListActivities") {
		t.Errorf("sfn wasn't listed: %v", f.calls)
	}
	// One search of the region, of two pages, for both services.
	if want := []string{"region:eu-west-2", "region:eu-west-2"}; !slices.Equal(f.queries, want) {
		t.Errorf("searches: got %q, want %q", f.queries, want)
	}
	if !strings.Contains(logs.String(), "aws: sqs: Resource Explorer's index has no sqs:queue in eu-west-2; not listing it") {
		t.Errorf("no log line for the skipped service:\n%s", logs)
	}
}

func TestResourceExplorerListsServicesWithATypeItDoesntIndex(t *testing.T) {
	f := newFakeExplorer()
	f.supported = []string{"ec2:instance", "sqs:queue", "states:stateMachine"}
	f.present = []string{"ec2:instance"}
	ctx, logs := useExplorer(t, f)

	listRegion(ctx, t, "sfn", true)

	// The region has no state machine, but the index can't see activities.
	if !slices.Contains(f.calls, "ListActivities") {
		t.Errorf("sfn wasn't listed: %v", f.calls)
	}
	if !strings.Contains(logs.String(), "Resource Explorer doesn't index states:activity") {
		t.Errorf("no log line for the unsupported type:\n%s", logs)
	}
}

func TestResourceExplorerFallsBackToListing(t *testing.T) {
	denied := `{"__type":"AccessDeniedException","message":"not allowed"}`
	tests := map[string]struct {
		change func(*fakeExplorer)
		want   string
	}{
		"no index": {func(f *fakeExplorer) {
			f.errs["/GetIndex"] = `{"__type":"ResourceNotFoundException","message":"index not found"}`
		}, "no index"},
		"GetIndex denied": {func(f *fakeExplorer) { f.errs["/GetIndex"] = denied }, "not allowed resource-explorer-2:GetIndex"},
		"Search unauthorized": {func(f *fakeExplorer) {
			f.errs["/Search"] = `{"__type":"UnauthorizedException","message":"no view"}`
		}, "not allowed resource-explorer-2:Search"},
		"index being built": {func(f *fakeExplorer) {
			f.indexes = []string{strings.Replace(explorerIndex("LOCAL", "2026-01-01T00:00:00Z", ""), "ACTIVE", "CREATING", 1)}
		}, "the index is CREATING"},
		"new index": {func(f *fakeExplorer) {
			f.indexes = []string{explorerIndex("LOCAL", explorerTestNow.Add(-time.Hour).Format(time.RFC3339), "")}
		}, "less than a day ago"},
		"no default view": {func(f *fakeExplorer) { f.views = []string{`{}`} }, "no default view"},
		"filtered view": {func(f *fakeExplorer) {
			f.view = strings.Replace(f.view, `"FilterString":""`, `"FilterString":"tag:env=prod"`, 1)
		}, `shows only "tag:env=prod"`},
		"view of an OU": {func(f *fakeExplorer) {
			f.view = strings.Replace(f.view, "arn:aws:iam::123456789012:root", "arn:aws:organizations::123456789012:ou/o-1/ou-1", 1)
		}, "not its account"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeExplorer()
			tt.change(f)
			ctx, logs := useExplorer(t, f)

			sqs := listRegion(ctx, t, "sqs", true)
			listRegion(ctx, t, "sfn", true)

			assertIDs(t, sqs, "aws_sqs_queue", "https://sqs.eu-west-2.amazonaws.com/123456789012/orders")
			if !slices.Contains(f.calls, "ListStateMachines") {
				t.Errorf("sfn wasn't listed: %v", f.calls)
			}
			// One line for the region, whatever the number of services.
			if n := strings.Count(logs.String(), "Resource Explorer can't rule out services in eu-west-2: "); n != 1 {
				t.Errorf("want one line saying why, got %d:\n%s", n, logs)
			}
			if !strings.Contains(logs.String(), tt.want) {
				t.Errorf("log doesn't say %q:\n%s", tt.want, logs)
			}
		})
	}
}

// The console's setup makes a default view only where the aggregator
// index is: the region's resources are searched there.
func TestResourceExplorerSearchesThroughTheAggregator(t *testing.T) {
	f := newFakeExplorer()
	f.indexes = []string{
		explorerIndex("LOCAL", "2026-01-01T00:00:00Z", `,"ReplicatingTo":["us-east-1"]`),
		explorerIndex("AGGREGATOR", "2026-01-01T00:00:00Z", `,"ReplicatingFrom":["eu-west-2","eu-west-1"],"LastUpdatedAt":"2026-02-01T00:00:00Z"`),
	}
	f.views = []string{`{}`, `{"ViewArn":"` + explorerViewARN + `"}`}
	ctx, _ := useExplorer(t, f)

	listRegion(ctx, t, "sqs", true)

	if slices.Contains(f.calls, "ListQueues") {
		t.Errorf("sqs was listed: %v", f.calls)
	}
	if f.gets["/GetIndex"] != 2 || f.gets["/GetDefaultView"] != 2 {
		t.Errorf("want the index and default view of both regions, got %v", f.gets)
	}
}

func TestResourceExplorerWithoutAnAggregatorListsEverything(t *testing.T) {
	f := newFakeExplorer()
	f.indexes = []string{
		explorerIndex("LOCAL", "2026-01-01T00:00:00Z", `,"ReplicatingTo":["us-east-1"]`),
		// us-east-1's index aggregates other regions only.
		explorerIndex("AGGREGATOR", "2026-01-01T00:00:00Z", `,"ReplicatingFrom":["eu-west-1"]`),
	}
	f.views = []string{`{}`, `{"ViewArn":"` + explorerViewARN + `"}`}
	ctx, logs := useExplorer(t, f)

	listRegion(ctx, t, "sqs", true)

	if !slices.Contains(f.calls, "ListQueues") {
		t.Errorf("sqs wasn't listed: %v", f.calls)
	}
	if !strings.Contains(logs.String(), "doesn't aggregate this region's") {
		t.Errorf("log doesn't say why:\n%s", logs)
	}
}

// A region with more resources than a search returns: a service none of
// whose types are among them is searched for on its own.
func TestResourceExplorerSearchesForTypesInALargeRegion(t *testing.T) {
	f := newFakeExplorer()
	f.present = []string{"ec2:instance"}
	f.more = []string{"states:activity"}
	f.complete = false
	ctx, _ := useExplorer(t, f)

	listRegion(ctx, t, "sqs", true)
	listRegion(ctx, t, "sfn", true)

	if slices.Contains(f.calls, "ListQueues") {
		t.Errorf("sqs was listed: %v", f.calls)
	}
	if !slices.Contains(f.calls, "ListActivities") {
		t.Errorf("sfn wasn't listed: %v", f.calls)
	}
	want := []string{
		"region:eu-west-2",
		// First, that a search for types finds one the region has.
		"region:eu-west-2 resourcetype:sqs:queue,ec2:instance",
		"region:eu-west-2 resourcetype:sqs:queue",
		"region:eu-west-2 resourcetype:states:activity,states:stateMachine",
	}
	if !slices.Equal(f.queries, want) {
		t.Errorf("searches: got %q, want %q", f.queries, want)
	}
}

// Two runs of one process, as a server's, each use Resource Explorer as
// they were told, and each reads its index for itself.
func TestResourceExplorerIsEachRunsOwn(t *testing.T) {
	t.Parallel()
	filtered, unfiltered := newFakeExplorer(), newFakeExplorer()
	filteredRun, unfilteredRun := explorerRun(t, filtered), explorerRun(t, unfiltered)

	listRegion(filteredRun, t, "sqs", true)
	sqs := listRegion(unfilteredRun, t, "sqs", false)
	listRegion(filteredRun, t, "sfn", true)

	if slices.Contains(filtered.calls, "ListQueues") || !slices.Contains(filtered.calls, "ListStateMachines") {
		t.Errorf("the run with Resource Explorer listed %v", filtered.calls)
	}
	assertIDs(t, sqs, "aws_sqs_queue", "https://sqs.eu-west-2.amazonaws.com/123456789012/orders")
	if len(unfiltered.gets) != 0 || len(unfiltered.queries) != 0 {
		t.Errorf("the run without Resource Explorer read its index: %v, searches %q", unfiltered.gets, unfiltered.queries)
	}
	// The filtered run read its index once, for both of its services.
	if filtered.gets["/GetIndex"] != 1 || len(filtered.queries) != 2 {
		t.Errorf("the run with Resource Explorer read its index %d times, searches %q", filtered.gets["/GetIndex"], filtered.queries)
	}
}

func TestResourceExplorerIsOffByDefault(t *testing.T) {
	t.Parallel()
	f := newFakeExplorer()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if strings.HasPrefix(call.Path, "/Get") || call.Path == "/Search" || call.Path == "/ListSupportedResourceTypes" {
			t.Errorf("called Resource Explorer: %s", call.Path)
		}
		return f.handle(call)
	})

	sqs := listRegion(ctx, t, "sqs", false)

	assertIDs(t, sqs, "aws_sqs_queue", "https://sqs.eu-west-2.amazonaws.com/123456789012/orders")
}

// Only regional services the provider has can be ruled out, by types in
// Resource Explorer's service:type notation.
func TestExplorerTypesAreOfRegionalServices(t *testing.T) {
	t.Parallel()
	services := (&Provider{}).Services()
	for name, types := range explorerTypes {
		if _, ok := services[name]; !ok {
			t.Errorf("%s: no such service", name)
		}
		if slices.Contains(GlobalServices, name) || slices.Contains(EastOnlyServices, name) {
			t.Errorf("%s: a global service", name)
		}
		if len(types) == 0 {
			t.Errorf("%s: no types", name)
		}
		for _, typ := range types {
			if service, resource, ok := strings.Cut(typ, ":"); !ok || service == "" || resource == "" || strings.Contains(typ, " ") || strings.Contains(typ, ",") {
				t.Errorf("%s: %q isn't a Resource Explorer type", name, typ)
			}
		}
	}
}
