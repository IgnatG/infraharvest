// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchevents"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Each fake API below serves two pages. The second page is returned when the
// request carries this token, so a lister that ignores pagination only sees
// the first page's resources.
const nextPageToken = "page-2-token"

func isSecondPage(call apiCall) bool {
	return strings.Contains(call.Body, nextPageToken) ||
		call.Query.Get("nextToken") == nextPageToken || call.Query.Get("marker") == nextPageToken
}

// pageOf returns "<prefix>-1" with a next-page token on the first page, and
// "<prefix>-2" with none on the second.
func pageOf(call apiCall, prefix string) (id, token string) {
	if isSecondPage(call) {
		return prefix + "-2", ""
	}
	return prefix + "-1", nextPageToken
}

func assertIDs(t *testing.T, resources []harvest.Resource, resourceType string, want ...string) {
	t.Helper()
	if got := resourceIDs(resources, resourceType); !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", resourceType, got, want)
	}
}

func TestPaginateByMarker(t *testing.T) {
	t.Parallel()
	pages := map[string]string{"": "m1", "m1": "m2", "m2": ""}
	var seen []string
	err := paginateByMarker(func(marker *string) (*string, error) {
		m := ""
		if marker != nil {
			m = *marker
		}
		seen = append(seen, m)
		next := pages[m]
		return &next, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"", "m1", "m2"}) {
		t.Errorf("pages requested: got %q", seen)
	}
}

func TestPaginateByMarkerStopsOnError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("throttled")
	err := paginateByMarker(func(*string) (*string, error) { return nil, wantErr })
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v, want %v", err, wantErr)
	}
}

func TestPaginateByMarkerRejectsRepeatedMarker(t *testing.T) {
	t.Parallel()
	calls := 0
	err := paginateByMarker(func(*string) (*string, error) {
		calls++
		if calls > 3 {
			t.Fatal("pagination loops forever on a repeated marker")
		}
		same := "m1"
		return &same, nil
	})
	if err == nil {
		t.Error("want an error when the marker does not advance")
	}
}

func TestSqsPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if !strings.Contains(call.Body, `"MaxResults":1000`) {
			t.Errorf("ListQueues must set MaxResults or AWS returns no NextToken; body: %s", call.Body)
		}
		id, token := pageOf(call, "queue")
		return fmt.Sprintf(`{"QueueUrls":["https://sqs.us-east-1.amazonaws.com/123456789012/%s"],"NextToken":%q}`, id, token)
	})
	g := &SqsLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_sqs_queue",
		"https://sqs.us-east-1.amazonaws.com/123456789012/queue-1",
		"https://sqs.us-east-1.amazonaws.com/123456789012/queue-2")
}

func TestKinesisPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		id, token := pageOf(call, "stream")
		return fmt.Sprintf(`{"StreamNames":[%q],"HasMoreStreams":%t,"NextToken":%q}`, id, token != "", token)
	})
	g := &KinesisLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_kinesis_stream", "stream-1", "stream-2")
}

func TestVpcEndpointPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		id, token := pageOf(call, "vpce")
		return fmt.Sprintf(`<DescribeVpcEndpointsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
			<vpcEndpointSet><item><vpcEndpointId>%s</vpcEndpointId></item></vpcEndpointSet>
			<nextToken>%s</nextToken></DescribeVpcEndpointsResponse>`, id, token)
	})
	g := &VpcEndpointLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_vpc_endpoint", "vpce-1", "vpce-2")
}

func TestBudgetsPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "GetCallerIdentity" {
			return `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
				<GetCallerIdentityResult><Account>123456789012</Account></GetCallerIdentityResult>
				</GetCallerIdentityResponse>`
		}
		id, token := pageOf(call, "budget")
		return fmt.Sprintf(`{"Budgets":[{"BudgetName":%q,"BudgetType":"COST","TimeUnit":"MONTHLY"}],"NextToken":%q}`, id, token)
	})
	g := &BudgetsLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_budgets_budget", "123456789012:budget-1", "123456789012:budget-2")
}

func TestCloud9Paginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "DescribeEnvironments" {
			return recordCloud9Describe(t, call, map[string]string{"env-1": "ec2", "env-2": "ec2"})
		}
		id, token := pageOf(call, "env")
		return fmt.Sprintf(`{"environmentIds":[%q],"nextToken":%q}`, id, token)
	})
	g := &Cloud9Lister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_cloud9_environment_ec2", "env-1", "env-2")
}

// wafListOps maps each WAF Classic list operation to the JSON keys of its
// result list and item ID. WAF and WAF Regional share these shapes.
var wafListOps = map[string][2]string{
	"ListWebACLs":               {"WebACLs", "WebACLId"},
	"ListByteMatchSets":         {"ByteMatchSets", "ByteMatchSetId"},
	"ListGeoMatchSets":          {"GeoMatchSets", "GeoMatchSetId"},
	"ListIPSets":                {"IPSets", "IPSetId"},
	"ListRateBasedRules":        {"Rules", "RuleId"},
	"ListRegexMatchSets":        {"RegexMatchSets", "RegexMatchSetId"},
	"ListRegexPatternSets":      {"RegexPatternSets", "RegexPatternSetId"},
	"ListRules":                 {"Rules", "RuleId"},
	"ListRuleGroups":            {"RuleGroups", "RuleGroupId"},
	"ListSizeConstraintSets":    {"SizeConstraintSets", "SizeConstraintSetId"},
	"ListSqlInjectionMatchSets": {"SqlInjectionMatchSets", "SqlInjectionMatchSetId"},
	"ListXssMatchSets":          {"XssMatchSets", "XssMatchSetId"},
}

var wafv2ListOps = map[string][2]string{
	"ListWebACLs":               {"WebACLs", "Id"},
	"ListIPSets":                {"IPSets", "Id"},
	"ListRegexPatternSets":      {"RegexPatternSets", "Id"},
	"ListRuleGroups":            {"RuleGroups", "Id"},
	"ListLoggingConfigurations": {"LoggingConfigurations", "ResourceArn"},
}

// serveWafPages answers each list operation in ops with one item per page.
// Item IDs are "<operation>-<page>" so every operation's pages can be checked.
func serveWafPages(t *testing.T, ops map[string][2]string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == "ListResourcesForWebACL" {
			return `{"ResourceArns":[]}`
		}
		keys, ok := ops[call.Op]
		if !ok {
			t.Errorf("unexpected operation %s", call.Op)
			return "{}"
		}
		id, token := pageOf(call, call.Op)
		return fmt.Sprintf(`{%q:[{%q:%q,"Name":"n","ARN":"arn:%s"}],"NextMarker":%q}`, keys[0], keys[1], id, id, token)
	}
}

func assertEveryOpPaginated(t *testing.T, resources []harvest.Resource, ops map[string][2]string) {
	t.Helper()
	got := map[string]bool{}
	for _, r := range resources {
		// WAFv2 IDs go on with the name and scope.
		got[strings.SplitN(r.Observed.ID, "/", 2)[0]] = true
	}
	for op := range ops {
		for _, id := range []string{op + "-1", op + "-2"} {
			if !got[id] {
				t.Errorf("%s: resource %s missing", op, id)
			}
		}
	}
}

func TestWafPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, serveWafPages(t, wafListOps))
	g := &WafLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafListOps)
}

func TestWafRegionalPaginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, serveWafPages(t, wafListOps))
	g := &WafRegionalLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafListOps)
}

func TestWafv2Paginates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, serveWafPages(t, wafv2ListOps))
	g := NewWafv2RegionalLister()
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafv2ListOps)
}

// Some endpoints, emulators among them, end the last page with an empty
// NextMarker instead of none. The rule lister used to loop on it forever.
func TestAlbListenerRulesStopOnEmptyMarker(t *testing.T) {
	t.Parallel()
	describeRules := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		const ns = `xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"`
		switch call.Op {
		case "DescribeLoadBalancers":
			return `<DescribeLoadBalancersResponse ` + ns + `><DescribeLoadBalancersResult><LoadBalancers><member>
				<LoadBalancerArn>arn:lb</LoadBalancerArn><LoadBalancerName>web</LoadBalancerName>
				</member></LoadBalancers></DescribeLoadBalancersResult></DescribeLoadBalancersResponse>`
		case "DescribeListeners":
			return `<DescribeListenersResponse ` + ns + `><DescribeListenersResult><Listeners><member>
				<ListenerArn>arn:listener</ListenerArn>
				</member></Listeners></DescribeListenersResult></DescribeListenersResponse>`
		case "DescribeRules":
			describeRules++
			if describeRules > 5 {
				t.Fatal("DescribeRules pagination does not stop")
			}
			id, token := pageOf(call, "arn:rule")
			return fmt.Sprintf(`<DescribeRulesResponse %s><DescribeRulesResult><Rules><member>
				<RuleArn>%s</RuleArn><IsDefault>false</IsDefault>
				</member></Rules><NextMarker>%s</NextMarker></DescribeRulesResult></DescribeRulesResponse>`, ns, id, token)
		case "DescribeListenerCertificates":
			return `<DescribeListenerCertificatesResponse ` + ns + `><DescribeListenerCertificatesResult><Certificates/>
				</DescribeListenerCertificatesResult></DescribeListenerCertificatesResponse>`
		case "DescribeTargetGroups":
			return `<DescribeTargetGroupsResponse ` + ns + `><DescribeTargetGroupsResult><TargetGroups/>
				</DescribeTargetGroupsResult></DescribeTargetGroupsResponse>`
		}
		t.Fatalf("unexpected call %s", call.Op)
		return ""
	})
	g := &AlbLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_lb_listener_rule", "arn:rule-1", "arn:rule-2")
}

// The target lister used to page with the rule list's token, which looped
// forever once there was more than one page of rules. Rules a service
// manages are left out, with their targets.
func TestCloudWatchEventTargetsPaginate(t *testing.T) {
	t.Parallel()
	calls := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		calls++
		if calls > 20 {
			t.Fatal("pagination does not stop")
		}
		switch call.Op {
		case "ListRules":
			id, token := pageOf(call, "rule")
			return fmt.Sprintf(`{"Rules":[{"Name":%q},{"Name":"managed-%s","ManagedBy":"states.amazonaws.com"}],"NextToken":%q}`, id, id, token)
		case "ListTargetsByRule":
			id, token := pageOf(call, "target")
			return fmt.Sprintf(`{"Targets":[{"Id":%q,"Arn":"arn:queue"}],"NextToken":%q}`, id, token)
		}
		t.Fatalf("unexpected call %s", call.Op)
		return ""
	})
	g := &CloudWatchLister{}
	g.SetContext(ctx)
	config, err := g.sdkConfig()
	if err != nil {
		t.Fatal(err)
	}

	if err := g.createRules(cloudwatchevents.NewFromConfig(config)); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_cloudwatch_event_rule", "rule-1", "rule-2")
	assertIDs(t, g.Resources, "aws_cloudwatch_event_target", "rule-1/target-1", "rule-1/target-2", "rule-2/target-1", "rule-2/target-2")
}

func TestStopOnDuplicateToken(t *testing.T) {
	t.Parallel()
	var options cloudcontrol.ListResourcesPaginatorOptions
	stopOnDuplicateToken(&options)
	if !options.StopOnDuplicateToken {
		t.Error("StopOnDuplicateToken not set")
	}
}

// Every SDK paginator must stop on a repeated token, or a service that
// returns the same token twice is listed forever.
func TestEveryPaginatorStopsOnDuplicateToken(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasPrefix(sel.Sel.Name, "New") || !strings.HasSuffix(sel.Sel.Name, "Paginator") || len(call.Args) == 0 {
				return true
			}
			if last, ok := call.Args[len(call.Args)-1].(*ast.Ident); !ok || last.Name != "stopOnDuplicateToken" {
				t.Errorf("%s: %s is not passed stopOnDuplicateToken", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

// A paginator does not advance after an error, so a lister that logged the
// error and went on asked for the same page forever.
func TestIamAccessKeysStopOnError(t *testing.T) {
	t.Parallel()
	calls := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		calls++
		if calls > 5 {
			t.Fatal("pagination does not stop on error")
		}
		const ns = `xmlns="https://iam.amazonaws.com/doc/2010-05-08/"`
		switch call.Op {
		case "GetAccountAuthorizationDetails":
			return `<GetAccountAuthorizationDetailsResponse ` + ns + `><GetAccountAuthorizationDetailsResult><UserDetailList><member>
				<UserName>alice</UserName><UserId>AIDA1</UserId><Path>/</Path>
				<Arn>arn:aws:iam::123456789012:user/alice</Arn>
				</member></UserDetailList><IsTruncated>false</IsTruncated></GetAccountAuthorizationDetailsResult></GetAccountAuthorizationDetailsResponse>`
		case "ListAccessKeys":
			return `<ErrorResponse ` + ns + `><Error><Code>Throttling</Code><Message>Rate exceeded</Message></Error></ErrorResponse>`
		}
		t.Fatalf("unexpected call %s", call.Op)
		return ""
	})
	g := &IamLister{}
	g.SetContext(ctx)

	err := g.List()

	if err == nil || !strings.Contains(err.Error(), "Throttling") {
		t.Errorf("want the ListAccessKeys error, got %v", err)
	}
}

func TestSnsSubscriptionsStopOnError(t *testing.T) {
	t.Parallel()
	calls := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		calls++
		if calls > 5 {
			t.Fatal("pagination does not stop on error")
		}
		const ns = `xmlns="http://sns.amazonaws.com/doc/2010-03-31/"`
		switch call.Op {
		case "ListTopics":
			return `<ListTopicsResponse ` + ns + `><ListTopicsResult><Topics><member>
				<TopicArn>arn:aws:sns:us-east-1:123456789012:alerts</TopicArn>
				</member></Topics></ListTopicsResult></ListTopicsResponse>`
		case "ListSubscriptionsByTopic":
			return `<ErrorResponse ` + ns + `><Error><Code>AuthorizationError</Code><Message>not allowed</Message></Error></ErrorResponse>`
		}
		t.Fatalf("unexpected call %s", call.Op)
		return ""
	})
	g := &SnsLister{}
	g.SetContext(ctx)

	err := g.List()

	if err == nil || !strings.Contains(err.Error(), "AuthorizationError") {
		t.Errorf("want the ListSubscriptionsByTopic error, got %v", err)
	}
}
