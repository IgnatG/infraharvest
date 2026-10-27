// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"sync"
	"testing"

	"github.com/aws/smithy-go/encoding/cbor"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// unwantedCase is a lister whose calls per listed resource list only
// resources of child types. Calls are told apart by their operation, or
// by their path where the protocol names no operation.
type unwantedCase struct {
	lister func() harvest.Lister
	// parent is the type the parent calls list, which is listed whatever
	// the import wants.
	parent string
	// parents answer the calls the lister makes whatever the import
	// wants, children those that list only resources of unwanted.
	parents, children map[string]string
	unwanted          []string
}

// unwantedCallKey is how unwantedCase names a call.
func unwantedCallKey(call apiCall) string {
	if call.Op != "" {
		return call.Op
	}
	return call.Path
}

// A lister asks for the parts of each resource it lists, a call or more
// per resource, only when the import may keep their type: one whose child
// types the import leaves out whatever is listed makes none of those
// calls, and still lists the parents. With every type wanted, it makes
// them.
func TestListersSkipTheCallsOfUnwantedTypes(t *testing.T) {
	const (
		wafACL    = "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/edge/a1"
		lambdaFn  = "arn:aws:lambda:us-east-1:123456789012:function:report"
		albWeb    = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/1"
		albGroup  = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/1"
		cwOps     = "/service/GraniteServiceVersion20100801/operation/"
		r53       = "/2013-04-01/"
		route53NS = `xmlns="https://route53.amazonaws.com/doc/2013-04-01/"`
	)
	for name, tc := range map[string]unwantedCase{
		"wafv2 web ACL associations": {
			lister: func() harvest.Lister { return NewWafv2RegionalLister() },
			parent: "aws_wafv2_web_acl",
			parents: map[string]string{
				"ListWebACLs":               `{"WebACLs":[{"Id":"a1","Name":"edge","ARN":"` + wafACL + `"}]}`,
				"ListIPSets":                `{"IPSets":[]}`,
				"ListRegexPatternSets":      `{"RegexPatternSets":[]}`,
				"ListRuleGroups":            `{"RuleGroups":[]}`,
				"ListLoggingConfigurations": `{"LoggingConfigurations":[]}`,
			},
			children: map[string]string{"ListResourcesForWebACL": `{"ResourceArns":[]}`},
			unwanted: []string{"aws_wafv2_web_acl_association"},
		},
		"KMS grants": {
			lister: func() harvest.Lister { return &KmsLister{} },
			parent: "aws_kms_key",
			parents: map[string]string{
				"ListAliases": `{"Aliases":[],"Truncated":false}`,
				"ListKeys":    `{"Keys":[{"KeyId":"k-app"}],"Truncated":false}`,
				"DescribeKey": `{"KeyMetadata":{"KeyId":"k-app","KeyManager":"CUSTOMER","KeyState":"Enabled"}}`,
			},
			children: map[string]string{"ListGrants": `{"Grants":[],"Truncated":false}`},
			unwanted: []string{"aws_kms_grant"},
		},
		"SNS subscriptions": {
			lister:   func() harvest.Lister { return &SnsLister{} },
			parent:   "aws_sns_topic",
			parents:  map[string]string{"ListTopics": recordQuery("ListTopics", recordSNSNS, `<Topics><member><TopicArn>`+recordSNSARN+`alerts</TopicArn></member></Topics>`)},
			children: map[string]string{"ListSubscriptionsByTopic": recordQuery("ListSubscriptionsByTopic", recordSNSNS, `<Subscriptions/>`)},
			unwanted: []string{"aws_sns_topic_subscription"},
		},
		"ECR repository policies": {
			lister: func() harvest.Lister { return &EcrLister{} },
			parent: "aws_ecr_repository",
			parents: map[string]string{
				"DescribeRepositories": `{"repositories":[{"repositoryName":"web","registryId":"123456789012"}]}`,
				"GetLifecyclePolicy":   recordJSONError("LifecyclePolicyNotFoundException"),
			},
			children: map[string]string{"GetRepositoryPolicy": recordJSONError("RepositoryPolicyNotFoundException")},
			unwanted: []string{"aws_ecr_repository_policy"},
		},
		"ECR policies": {
			lister:  func() harvest.Lister { return &EcrLister{} },
			parent:  "aws_ecr_repository",
			parents: map[string]string{"DescribeRepositories": `{"repositories":[{"repositoryName":"web","registryId":"123456789012"}]}`},
			children: map[string]string{
				"GetRepositoryPolicy": recordJSONError("RepositoryPolicyNotFoundException"),
				"GetLifecyclePolicy":  recordJSONError("LifecyclePolicyNotFoundException"),
			},
			unwanted: []string{"aws_ecr_repository_policy", "aws_ecr_lifecycle_policy"},
		},
		"Glue tables": {
			lister: func() harvest.Lister { return &GlueLister{} },
			parent: "aws_glue_catalog_database",
			parents: map[string]string{
				"GetCallerIdentity": recordQuery("GetCallerIdentity", recordGlueSTSNS, `<Account>123456789012</Account>`),
				"GetCrawlers":       `{"Crawlers":[]}`,
				"GetDatabases":      `{"DatabaseList":[{"Name":"sales"}]}`,
				"GetJobs":           `{"Jobs":[]}`,
				"GetTriggers":       `{"Triggers":[]}`,
			},
			children: map[string]string{"GetTables": `{"TableList":[]}`},
			unwanted: []string{"aws_glue_catalog_table"},
		},
		"Cognito user pool clients": {
			lister: func() harvest.Lister { return &CognitoLister{} },
			parent: "aws_cognito_user_pool",
			parents: map[string]string{
				"ListIdentityPools": `{"IdentityPools":[]}`,
				"ListUserPools":     `{"UserPools":[{"Id":"us-east-1_AAA","Name":"users"}]}`,
			},
			children: map[string]string{"ListUserPoolClients": `{"UserPoolClients":[]}`},
			unwanted: []string{"aws_cognito_user_pool_client"},
		},
		"identity store group memberships": {
			lister: func() harvest.Lister { return &IdentityStoreLister{} },
			parent: "aws_identitystore_group",
			parents: map[string]string{
				"ListInstances": `{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-1"}]}`,
				"ListUsers":     `{"Users":[]}`,
				"ListGroups":    `{"Groups":[{"GroupId":"g-1","IdentityStoreId":"d-1","DisplayName":"admins"}]}`,
			},
			children: map[string]string{"ListGroupMemberships": `{"GroupMemberships":[]}`},
			unwanted: []string{"aws_identitystore_group_membership"},
		},
		"EventBridge targets": {
			lister: func() harvest.Lister { return &CloudWatchLister{} },
			parent: "aws_cloudwatch_event_rule",
			parents: map[string]string{
				cwOps + "DescribeAlarms": recordCloudWatchPage("MetricAlarms", cbor.List{}, ""),
				cwOps + "ListDashboards": recordCloudWatchPage("DashboardEntries", cbor.List{}, ""),
				"ListRules":              `{"Rules":[{"Name":"nightly","State":"ENABLED"}]}`,
			},
			children: map[string]string{"ListTargetsByRule": `{"Targets":[]}`},
			unwanted: []string{"aws_cloudwatch_event_target"},
		},
		"CloudFormation stack set instances": {
			lister: func() harvest.Lister { return &CloudFormationLister{} },
			parent: "aws_cloudformation_stack_set",
			parents: map[string]string{
				"ListStacks":    recordQuery("ListStacks", recordCfnNS, `<StackSummaries/>`),
				"ListStackSets": recordQuery("ListStackSets", recordCfnNS, `<Summaries><member><StackSetName>baseline</StackSetName><StackSetId>baseline:1</StackSetId><Status>ACTIVE</Status></member></Summaries>`),
			},
			children: map[string]string{"ListStackInstances": recordQuery("ListStackInstances", recordCfnNS, `<Summaries/>`)},
			unwanted: []string{"aws_cloudformation_stack_set_instance"},
		},
		"SES receipt rules": {
			lister: func() harvest.Lister { return &SesLister{} },
			parent: "aws_ses_receipt_rule_set",
			parents: map[string]string{
				"ListIdentities":        recordQuery("ListIdentities", recordSESNS, `<Identities/>`),
				"ListTemplates":         recordQuery("ListTemplates", recordSESNS, `<TemplatesMetadata/>`),
				"ListConfigurationSets": recordQuery("ListConfigurationSets", recordSESNS, `<ConfigurationSets/>`),
				"ListReceiptRuleSets":   recordQuery("ListReceiptRuleSets", recordSESNS, `<RuleSets><member><Name>inbound</Name></member></RuleSets>`),
			},
			children: map[string]string{"DescribeReceiptRuleSet": recordQuery("DescribeReceiptRuleSet", recordSESNS, `<Metadata><Name>inbound</Name></Metadata><Rules/>`)},
			unwanted: []string{"aws_ses_receipt_rule"},
		},
		"Route 53 records": {
			lister: func() harvest.Lister { return &Route53Lister{} },
			parent: "aws_route53_zone",
			parents: map[string]string{
				r53 + "hostedzone": `<ListHostedZonesResponse ` + route53NS + `><HostedZones>
					<HostedZone><Id>/hostedzone/Z1</Id><Name>example.com.</Name><CallerReference>c-1</CallerReference></HostedZone>
					</HostedZones><IsTruncated>false</IsTruncated><MaxItems>100</MaxItems></ListHostedZonesResponse>`,
				r53 + "healthcheck": `<ListHealthChecksResponse ` + route53NS + `><HealthChecks/><IsTruncated>false</IsTruncated><Marker></Marker><MaxItems>100</MaxItems></ListHealthChecksResponse>`,
			},
			children: map[string]string{r53 + "hostedzone/Z1/rrset": `<ListResourceRecordSetsResponse ` + route53NS + `><ResourceRecordSets/><IsTruncated>false</IsTruncated><MaxItems>300</MaxItems></ListResourceRecordSetsResponse>`},
			unwanted: []string{"aws_route53_record"},
		},
		"load balancer listeners and targets": {
			lister: func() harvest.Lister { return &AlbLister{} },
			parent: "aws_lb",
			parents: map[string]string{
				"DescribeLoadBalancers": recordQuery("DescribeLoadBalancers", recordAlbNS, `<LoadBalancers><member><LoadBalancerArn>`+albWeb+`</LoadBalancerArn><LoadBalancerName>web</LoadBalancerName><Type>application</Type></member></LoadBalancers>`),
				"DescribeTargetGroups":  recordQuery("DescribeTargetGroups", recordAlbNS, `<TargetGroups><member><TargetGroupArn>`+albGroup+`</TargetGroupArn><TargetGroupName>web</TargetGroupName></member></TargetGroups>`),
			},
			children: map[string]string{
				"DescribeListeners":    recordQuery("DescribeListeners", recordAlbNS, `<Listeners/>`),
				"DescribeTargetHealth": recordQuery("DescribeTargetHealth", recordAlbNS, `<TargetHealthDescriptions/>`),
			},
			unwanted: []string{"aws_lb_listener", "aws_lb_listener_rule", "aws_lb_listener_certificate", "aws_lb_target_group_attachment"},
		},
		"EFS mount targets and policies": {
			lister: func() harvest.Lister { return &EfsLister{} },
			parent: "aws_efs_file_system",
			parents: map[string]string{
				"/2015-02-01/file-systems":  `{"FileSystems":[{"FileSystemId":"fs-1"}]}`,
				"/2015-02-01/access-points": `{"AccessPoints":[]}`,
			},
			children: map[string]string{
				"/2015-02-01/mount-targets":            `{"MountTargets":[]}`,
				"/2015-02-01/file-systems/fs-1/policy": `{"FileSystemId":"fs-1"}`,
			},
			unwanted: []string{"aws_efs_mount_target", "aws_efs_file_system_policy"},
		},
		"Lambda permissions and event invoke configs": {
			lister: func() harvest.Lister { return &LambdaLister{} },
			parent: "aws_lambda_function",
			parents: map[string]string{
				"/2015-03-31/functions":             `{"Functions":[{"FunctionName":"report","FunctionArn":"` + lambdaFn + `"}]}`,
				"/2015-03-31/event-source-mappings": `{"EventSourceMappings":[]}`,
				"/2018-10-31/layers":                `{"Layers":[]}`,
			},
			children: map[string]string{
				"/2015-03-31/functions/" + lambdaFn + "/policy":         recordJSONError("ResourceNotFoundException"),
				"/2019-09-25/functions/report/event-invoke-config/list": `{"FunctionEventInvokeConfigs":[]}`,
			},
			unwanted: []string{"aws_lambda_permission", "aws_lambda_function_event_invoke_config"},
		},
		"EKS cluster parts": {
			lister:  func() harvest.Lister { return &EksLister{} },
			parent:  "aws_eks_cluster",
			parents: map[string]string{"/clusters": `{"clusters":["c1"]}`},
			children: map[string]string{
				"/clusters/c1/node-groups":               `{"nodegroups":[]}`,
				"/clusters/c1/addons":                    `{"addons":[]}`,
				"/clusters/c1/fargate-profiles":          `{"fargateProfileNames":[]}`,
				"/clusters/c1/pod-identity-associations": `{"associations":[]}`,
				"/clusters/c1":                           `{"cluster":{"name":"c1","accessConfig":{"authenticationMode":"API"}}}`,
				"/clusters/c1/access-entries":            `{"accessEntries":[]}`,
			},
			unwanted: []string{"aws_eks_node_group", "aws_eks_addon", "aws_eks_fargate_profile", "aws_eks_pod_identity_association", "aws_eks_access_entry", "aws_eks_access_policy_association"},
		},
		"API Gateway v2 API parts": {
			lister: func() harvest.Lister { return &APIGatewayV2Lister{} },
			parent: "aws_apigatewayv2_api",
			parents: map[string]string{
				"/v2/apis":     `{"items":[{"apiId":"a1","name":"chat","protocolType":"WEBSOCKET"}]}`,
				"/v2/vpclinks": `{"items":[]}`,
			},
			children: map[string]string{
				"/v2/apis/a1/stages":       `{"items":[]}`,
				"/v2/apis/a1/models":       `{"items":[]}`,
				"/v2/apis/a1/integrations": `{"items":[]}`,
				"/v2/apis/a1/routes":       `{"items":[]}`,
				"/v2/apis/a1/authorizers":  `{"items":[]}`,
			},
			unwanted: []string{"aws_apigatewayv2_stage", "aws_apigatewayv2_model", "aws_apigatewayv2_integration", "aws_apigatewayv2_route", "aws_apigatewayv2_route_response", "aws_apigatewayv2_authorizer"},
		},
		"API Gateway REST API parts": {
			lister: func() harvest.Lister { return &APIGatewayLister{} },
			parent: "aws_api_gateway_rest_api",
			parents: map[string]string{
				"/restapis":   `{"item":[{"id":"r1","name":"orders"}]}`,
				"/vpclinks":   `{"item":[]}`,
				"/usageplans": `{"item":[]}`,
				"/apikeys":    `{"item":[]}`,
			},
			children: map[string]string{
				"/restapis/r1/stages":              `{"item":[]}`,
				"/restapis/r1/resources":           `{"item":[]}`,
				"/restapis/r1/models":              `{"item":[]}`,
				"/restapis/r1/gatewayresponses":    `{"item":[]}`,
				"/restapis/r1/documentation/parts": `{"item":[]}`,
				"/restapis/r1/authorizers":         `{"item":[]}`,
			},
			unwanted: []string{"aws_api_gateway_stage", "aws_api_gateway_deployment", "aws_api_gateway_resource", "aws_api_gateway_method", "aws_api_gateway_integration", "aws_api_gateway_integration_response", "aws_api_gateway_method_response", "aws_api_gateway_model", "aws_api_gateway_gateway_response", "aws_api_gateway_documentation_part", "aws_api_gateway_authorizer"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			children := 0
			useFakeAPI(t, func(call apiCall) string {
				key := unwantedCallKey(call)
				if answer, ok := tc.parents[key]; ok {
					return answer
				}
				if answer, ok := tc.children[key]; ok {
					mu.Lock()
					defer mu.Unlock()
					children++
					return answer
				}
				return recordUnexpected(t, call)
			})
			list := func(unwanted []string) []harvest.Resource {
				t.Helper()
				p := &Provider{}
				p.SetUnwantedTypes(func(typ string) bool { return slices.Contains(unwanted, typ) })
				g := p.withUnwanted(tc.lister())
				if err := g.List(); err != nil {
					t.Fatal(err)
				}
				return g.Listed()
			}

			list(nil)
			if children == 0 {
				t.Fatal("with every type wanted, the lister didn't look for the children")
			}
			children = 0
			resources := list(tc.unwanted)
			if children != 0 {
				t.Errorf("made %d calls for %v, which the import leaves out", children, tc.unwanted)
			}
			if len(resourceIDs(resources, tc.parent)) == 0 {
				t.Errorf("didn't list the %s", tc.parent)
			}
			for _, r := range resources {
				if slices.Contains(tc.unwanted, r.Address.Type) {
					t.Errorf("listed %s %s, which the import leaves out", r.Address.Type, r.Observed.ID)
				}
			}
		})
	}
}

// The WAF Regional lister asks which resources each web ACL protects, a
// call per web ACL and resource type, only when the import may keep the
// associations.
func TestWafRegionalSkipsUnwantedAssociations(t *testing.T) {
	for _, unwanted := range []bool{false, true} {
		lists := recordWafAPI(t)
		var mu sync.Mutex
		asked := 0
		useFakeAPI(t, func(call apiCall) string {
			if call.Op != "ListResourcesForWebACL" {
				return lists(call)
			}
			mu.Lock()
			defer mu.Unlock()
			asked++
			return `{"ResourceArns":[]}`
		})
		p := &Provider{}
		p.SetUnwantedTypes(func(typ string) bool { return unwanted && typ == "aws_wafregional_web_acl_association" })
		g := p.withUnwanted(&WafRegionalLister{})
		if err := g.List(); err != nil {
			t.Fatal(err)
		}
		if len(resourceIDs(g.Listed(), "aws_wafregional_web_acl")) == 0 {
			t.Fatal("no web ACLs listed")
		}
		if got := asked > 0; got == unwanted {
			t.Errorf("associations unwanted %v: asked for them %d times", unwanted, asked)
		}
	}
}
