package aws

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
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

func assertIDs(t *testing.T, resources []terraformutils.Resource, resourceType string, want ...string) {
	t.Helper()
	if got := resourceIDs(resources, resourceType); !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", resourceType, got, want)
	}
}

func TestPaginateByMarker(t *testing.T) {
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
	wantErr := errors.New("throttled")
	err := paginateByMarker(func(*string) (*string, error) { return nil, wantErr })
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v, want %v", err, wantErr)
	}
}

func TestPaginateByMarkerRejectsRepeatedMarker(t *testing.T) {
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
	useFakeAPI(t, func(call apiCall) string {
		if !strings.Contains(call.Body, `"MaxResults":1000`) {
			t.Errorf("ListQueues must set MaxResults or AWS returns no NextToken; body: %s", call.Body)
		}
		id, token := pageOf(call, "queue")
		return fmt.Sprintf(`{"QueueUrls":["https://sqs.us-east-1.amazonaws.com/123456789012/%s"],"NextToken":%q}`, id, token)
	})
	g := &SqsGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_sqs_queue",
		"https://sqs.us-east-1.amazonaws.com/123456789012/queue-1",
		"https://sqs.us-east-1.amazonaws.com/123456789012/queue-2")
}

func TestKinesisPaginates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		id, token := pageOf(call, "stream")
		return fmt.Sprintf(`{"StreamNames":[%q],"HasMoreStreams":%t,"NextToken":%q}`, id, token != "", token)
	})
	g := &KinesisGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_kinesis_stream", "stream-1", "stream-2")
}

func TestVpcEndpointPaginates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		id, token := pageOf(call, "vpce")
		return fmt.Sprintf(`<DescribeVpcEndpointsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
			<vpcEndpointSet><item><vpcEndpointId>%s</vpcEndpointId></item></vpcEndpointSet>
			<nextToken>%s</nextToken></DescribeVpcEndpointsResponse>`, id, token)
	})
	g := &VpcEndpointGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_vpc_endpoint", "vpce-1", "vpce-2")
}

func TestIotPaginates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Path {
		case "/thing-types":
			id, token := pageOf(call, "type")
			return fmt.Sprintf(`{"thingTypes":[{"thingTypeName":%q}],"nextToken":%q}`, id, token)
		case "/things":
			id, token := pageOf(call, "thing")
			return fmt.Sprintf(`{"things":[{"thingName":%q}],"nextToken":%q}`, id, token)
		case "/rules":
			id, token := pageOf(call, "rule")
			return fmt.Sprintf(`{"rules":[{"ruleName":%q}],"nextToken":%q}`, id, token)
		case "/role-aliases":
			id, token := pageOf(call, "alias")
			return fmt.Sprintf(`{"roleAliases":[%q],"nextMarker":%q}`, id, token)
		}
		t.Errorf("unexpected request %s", call.Path)
		return "{}"
	})
	g := &IotGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_iot_thing_type", "type-1", "type-2")
	assertIDs(t, g.Resources, "aws_iot_thing", "thing-1", "thing-2")
	assertIDs(t, g.Resources, "aws_iot_topic_rule", "rule-1", "rule-2")
	assertIDs(t, g.Resources, "aws_iot_role_alias", "alias-1", "alias-2")
}

func TestBudgetsPaginates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op == "GetCallerIdentity" {
			return `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
				<GetCallerIdentityResult><Account>123456789012</Account></GetCallerIdentityResult>
				</GetCallerIdentityResponse>`
		}
		id, token := pageOf(call, "budget")
		return fmt.Sprintf(`{"Budgets":[{"BudgetName":%q,"BudgetType":"COST","TimeUnit":"MONTHLY"}],"NextToken":%q}`, id, token)
	})
	g := &BudgetsGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_budgets_budget", "123456789012:budget-1", "123456789012:budget-2")
}

func TestCloud9Paginates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op == "DescribeEnvironmentStatus" {
			return `{"status":"ready","message":"ok"}`
		}
		id, token := pageOf(call, "env")
		return fmt.Sprintf(`{"environmentIds":[%q],"nextToken":%q}`, id, token)
	})
	g := &Cloud9Generator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_cloud9_environment_ec2", "env-1", "env-2")
}

func TestCloud9ReturnsStatusError(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op == "DescribeEnvironmentStatus" {
			return `{"__type":"AccessDeniedException","message":"denied"}`
		}
		return `{"environmentIds":["env-1"]}`
	})
	g := &Cloud9Generator{}

	// Before the fix, the ignored error left details nil and this panicked.
	if err := g.InitResources(); err == nil {
		t.Error("want the DescribeEnvironmentStatus error")
	}
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

func assertEveryOpPaginated(t *testing.T, resources []terraformutils.Resource, ops map[string][2]string) {
	t.Helper()
	got := map[string]bool{}
	for _, r := range resources {
		got[r.InstanceState.ID] = true
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
	useFakeAPI(t, serveWafPages(t, wafListOps))
	g := &WafGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafListOps)
}

func TestWafRegionalPaginates(t *testing.T) {
	useFakeAPI(t, serveWafPages(t, wafListOps))
	g := &WafRegionalGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafListOps)
}

func TestWafv2Paginates(t *testing.T) {
	useFakeAPI(t, serveWafPages(t, wafv2ListOps))
	g := NewWafv2RegionalGenerator()

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertEveryOpPaginated(t, g.Resources, wafv2ListOps)
}
