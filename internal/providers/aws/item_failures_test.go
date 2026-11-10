// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"testing"

	"github.com/aws/smithy-go/encoding/cbor"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// itemFailureCase is a lister that makes calls of its own for each
// resource it lists, such as a topic's subscriptions, and how a fake AWS
// answers it: handle lists two resources, item-a and item-b, and answers
// every other call empty; the calls for item-b alone fail with the code
// fail gives it.
type itemFailureCase struct {
	name   string
	lister func() harvest.Lister
	// handle answers call; fail is the error answer for a call about
	// item-b, in the protocol of the call.
	handle func(call apiCall, fail func(code string) string) string
	// denied and gone are the codes the calls for item-b fail with: one
	// that fails the call, and one that says item-b no longer exists.
	// An empty gone skips that case, for an API whose code for it isn't
	// one awsCodes reads as gone.
	denied, gone string
	// typ is the type of the listed resources, and keepsFailed whether
	// item-b is kept when its calls fail, as it is but for EKS, which
	// leaves out a cluster it can't read whole.
	typ         string
	keepsFailed bool
}

// TestItemFailures checks that a call about one resource that fails
// leaves the others listed: a resource deleted since it was listed is no
// failure, and any other failure makes the listing partial, keeping what
// was listed.
func TestItemFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range itemFailureCases() {
		for _, code := range []string{tc.denied, tc.gone} {
			if code == "" {
				continue
			}
			t.Run(tc.name+"/"+code, func(t *testing.T) {
				t.Parallel()
				ctx := useFakeAPI(t, func(call apiCall) string {
					return tc.handle(call, func(fallback string) string {
						if isItemB(call) {
							return itemError(call, code)
						}
						return fallback
					})
				})
				resources, err := listChecked(ctx, t, tc.lister(), harvest.Settings{})
				var listed []string
				for _, r := range resources {
					// Some services name a resource <name>_<ID>.
					if r.Address.Type == tc.typ && strings.HasPrefix(r.RawName, "item-") {
						listed = append(listed, r.RawName[:len("item-a")])
					}
				}
				want := []string{"item-a", "item-b"}
				if code == tc.gone {
					if err != nil {
						t.Fatalf("a resource deleted since it was listed failed the listing: %v", err)
					}
					if !tc.keepsFailed {
						want = []string{"item-a"}
					}
				} else {
					assertRecordedError(t, err, code)
					if !harvest.IsPartial(err) {
						t.Errorf("the listing failed whole, want it partial: %v", err)
					}
					if !tc.keepsFailed {
						want = []string{"item-a"}
					}
				}
				if !slices.Equal(listed, want) {
					t.Errorf("listed %s %v, want %v", tc.typ, listed, want)
				}
			})
		}
	}
}

// isItemB reports whether call asks about item-b.
func isItemB(call apiCall) bool {
	return strings.Contains(call.Path+"?"+call.Query.Encode()+" "+call.Body, "item-b")
}

// itemError is an error answer with code in the protocol of call.
func itemError(call apiCall, code string) string {
	if call.Protocol == protocolQuery {
		return recordQueryError("https://example.amazonaws.com/doc/", code)
	}
	return recordJSONError(code)
}

func itemFailureCases() []itemFailureCase {
	const lambdaARN = "arn:aws:lambda:us-east-1:123456789012:function:"
	const cfnNS = "http://cloudformation.amazonaws.com/doc/2010-05-15/"
	const sesNS = "http://ses.amazonaws.com/doc/2010-12-01/"
	return []itemFailureCase{
		{
			name: "lambda", lister: func() harvest.Lister { return &LambdaLister{} },
			typ: "aws_lambda_function", keepsFailed: true,
			denied: "AccessDeniedException", gone: "ResourceNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch {
				case call.Path == "/2015-03-31/functions":
					return `{"Functions":[{"FunctionName":"item-a","FunctionArn":"` + lambdaARN + `item-a"},
						{"FunctionName":"item-b","FunctionArn":"` + lambdaARN + `item-b"}]}`
				case strings.HasSuffix(call.Path, "/policy"):
					return fail(`{"Policy":"{\"Statement\":[]}"}`)
				}
				return fail(`{}`)
			},
		},
		{
			name: "cloudwatch", lister: func() harvest.Lister { return &CloudWatchLister{} },
			typ: "aws_cloudwatch_event_rule", keepsFailed: true,
			denied: "AccessDeniedException", gone: "ResourceNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "DescribeAlarms":
					return recordCloudWatchPage("MetricAlarms", cbor.List{}, "")
				case "ListDashboards":
					return recordCloudWatchPage("DashboardEntries", cbor.List{}, "")
				case "ListRules":
					return `{"Rules":[{"Name":"item-a"},{"Name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "apigateway", lister: func() harvest.Lister { return &APIGatewayLister{} },
			typ: "aws_api_gateway_rest_api", keepsFailed: true,
			denied: "AccessDeniedException", gone: "NotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Path == "/restapis" {
					return `{"item":[{"id":"item-a","name":"item-a"},{"id":"item-b","name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "apigatewayv2", lister: func() harvest.Lister { return &APIGatewayV2Lister{} },
			typ: "aws_apigatewayv2_api", keepsFailed: true,
			denied: "AccessDeniedException", gone: "NotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Path == "/v2/apis" {
					return `{"items":[{"apiId":"item-a","name":"item-a","protocolType":"WEBSOCKET"},
						{"apiId":"item-b","name":"item-b","protocolType":"WEBSOCKET"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "cloudformation", lister: func() harvest.Lister { return &CloudFormationLister{} },
			typ: "aws_cloudformation_stack_set", keepsFailed: true,
			denied: "AccessDenied", gone: "StackSetNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "ListStacks":
					return recordQuery(call.Op, cfnNS, `<StackSummaries/>`)
				case "ListStackSets":
					return recordQuery(call.Op, cfnNS, `<Summaries>
						<member><StackSetName>item-a</StackSetName><Status>ACTIVE</Status></member>
						<member><StackSetName>item-b</StackSetName><Status>ACTIVE</Status></member>
						</Summaries>`)
				}
				return fail(recordQuery(call.Op, cfnNS, `<Summaries/>`))
			},
		},
		{
			name: "cognito", lister: func() harvest.Lister { return &CognitoLister{} },
			typ: "aws_cognito_user_pool", keepsFailed: true,
			denied: "AccessDeniedException", gone: "ResourceNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "ListIdentityPools":
					return `{"IdentityPools":[]}`
				case "ListUserPools":
					return `{"UserPools":[{"Id":"item-a","Name":"item-a"},{"Id":"item-b","Name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "eks", lister: func() harvest.Lister { return &EksLister{} },
			typ: "aws_eks_cluster", keepsFailed: false,
			denied: "AccessDeniedException", gone: "ResourceNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Path == "/clusters" {
					return `{"clusters":["item-a","item-b"]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "glue", lister: func() harvest.Lister { return &GlueLister{} },
			typ: "aws_glue_catalog_database", keepsFailed: true,
			denied: "AccessDeniedException", gone: "EntityNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "GetCallerIdentity":
					return recordQuery(call.Op, recordGlueSTSNS, `<Account>123456789012</Account>`)
				case "GetDatabases":
					return `{"DatabaseList":[{"Name":"item-a"},{"Name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "identitystore", lister: func() harvest.Lister { return &IdentityStoreLister{} },
			typ: "aws_identitystore_group", keepsFailed: true,
			denied: "AccessDeniedException", gone: "ResourceNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "ListInstances":
					return `{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-1"}]}`
				case "ListGroups":
					return `{"Groups":[{"IdentityStoreId":"d-1","GroupId":"item-a","DisplayName":"item-a"},
						{"IdentityStoreId":"d-1","GroupId":"item-b","DisplayName":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			// SES says a rule set is gone with RuleSetDoesNotExist, which
			// awsCodes doesn't read as gone: only the failure is checked.
			name: "ses", lister: func() harvest.Lister { return &SesLister{} },
			typ: "aws_ses_receipt_rule_set", keepsFailed: true,
			denied: "AccessDenied",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Op == "ListReceiptRuleSets" {
					return recordQuery(call.Op, sesNS, `<RuleSets>
						<member><Name>item-a</Name></member><member><Name>item-b</Name></member>
						</RuleSets>`)
				}
				return fail(recordQuery(call.Op, sesNS, ``))
			},
		},
		{
			// A web ACL gone since it was listed answers
			// WAFNonexistentItemException, which the lister skips itself.
			name: "wafregional", lister: func() harvest.Lister { return &WafRegionalLister{} },
			typ: "aws_wafregional_web_acl", keepsFailed: true,
			denied: "AccessDeniedException",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Op == "ListWebACLs" {
					return `{"WebACLs":[{"WebACLId":"item-a","Name":"item-a"},{"WebACLId":"item-b","Name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "wafv2", lister: func() harvest.Lister { return NewWafv2RegionalLister() },
			typ: "aws_wafv2_web_acl", keepsFailed: true,
			denied: "AccessDeniedException",
			handle: func(call apiCall, fail func(string) string) string {
				if call.Op == "ListWebACLs" {
					return `{"WebACLs":[
						{"Id":"1","Name":"item-a","ARN":"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/item-a/1"},
						{"Id":"2","Name":"item-b","ARN":"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/item-b/2"}]}`
				}
				return fail(`{}`)
			},
		},
		{
			name: "organizations", lister: func() harvest.Lister { return &OrganizationLister{} },
			typ: "aws_organizations_policy", keepsFailed: true,
			denied: "AccessDeniedException", gone: "PolicyNotFoundException",
			handle: func(call apiCall, fail func(string) string) string {
				switch call.Op {
				case "ListRoots":
					return `{"Roots":[]}`
				case "ListPolicies":
					return `{"Policies":[{"Id":"item-a","Name":"item-a"},{"Id":"item-b","Name":"item-b"}]}`
				}
				return fail(`{}`)
			},
		},
	}
}
