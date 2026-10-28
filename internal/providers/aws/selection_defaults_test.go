// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

const ec2NS = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`

// noStacks answers ListStacks for a region without CloudFormation stacks.
const noStacks = `<ListStacksResponse xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><ListStacksResult><StackSummaries/></ListStacksResult></ListStacksResponse>`

// defaultNetworkAPI answers like a region whose default VPC is vpc-default.
func defaultNetworkAPI(call apiCall) string {
	switch call.Op {
	case "DescribeVpcs":
		return `<DescribeVpcsResponse ` + ec2NS + `><vpcSet><item><vpcId>vpc-default</vpcId></item></vpcSet></DescribeVpcsResponse>`
	case "DescribeSubnets":
		return `<DescribeSubnetsResponse ` + ec2NS + `><subnetSet><item><subnetId>subnet-default-a</subnetId></item></subnetSet></DescribeSubnetsResponse>`
	case "DescribeRouteTables":
		return `<DescribeRouteTablesResponse ` + ec2NS + `><routeTableSet><item><routeTableId>rtb-default</routeTableId></item></routeTableSet></DescribeRouteTablesResponse>`
	case "DescribeInternetGateways":
		return `<DescribeInternetGatewaysResponse ` + ec2NS + `><internetGatewaySet><item><internetGatewayId>igw-default</internetGatewayId></item></internetGatewaySet></DescribeInternetGatewaysResponse>`
	case "ListStacks":
		return noStacks
	}
	return `<Response><Errors><Error><Code>Unexpected</Code></Error></Errors></Response>`
}

// The provider leaves out the default network and the security groups EKS
// creates for clusters; the catalogue's skip rules cover the rest, such as
// default network ACLs, default security groups as AWS created them and
// service-linked roles. A VPC's default security group is not asked for:
// the sg lister records whether it is as AWS created it.
func TestExcludedByDefault(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, defaultNetworkAPI)
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{
		harvest.RecordBare("vpc-default", "vpc-default", "aws_vpc", "aws"),
		harvest.RecordBare("vpc-0abc1234", "main", "aws_vpc", "aws"),
		harvest.RecordBare("subnet-default-a", "a", "aws_subnet", "aws"),
		harvest.RecordBare("subnet-0own", "b", "aws_subnet", "aws"),
		harvest.RecordBare("rtb-default", "main", "aws_route_table", "aws"),
		harvest.RecordBare("igw-default", "gw", "aws_internet_gateway", "aws"),
		harvest.Record("sg-default-own", "default", "aws_security_group", "aws", map[string]string{"group_name": "default", "as_created": "true"}),
		harvest.Record("sgr-1", "default_ingress_all", "aws_vpc_security_group_ingress_rule", "aws", map[string]string{"security_group_id": "sg-default-own", "as_created": "true"}),
		harvest.Record("sg-eks", "eks-cluster-sg-web-1", "aws_security_group", "aws", map[string]string{eksClusterTag: "web"}),
		harvest.Record("sgr-2", "eks_egress_all", "aws_vpc_security_group_egress_rule", "aws", map[string]string{"security_group_id": "sg-eks"}),
		harvest.Record("sg-0own", "web", "aws_security_group", "aws", map[string]string{"tags.Name": "web"}),
		harvest.RecordBare("acl-1", "acl-1", "aws_default_network_acl", "aws"),
		harvest.RecordBare("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws"),
		harvest.RecordBare("/aws/lambda/fn", "fn", "aws_cloudwatch_log_group", "aws"),
		harvest.RecordBare("/app/web", "web", "aws_cloudwatch_log_group", "aws"),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_vpc vpc-default":                      reasonDefaultVPC,
		"aws_subnet subnet-default-a":              reasonDefaultNetwork,
		"aws_route_table rtb-default":              reasonDefaultNetwork,
		"aws_internet_gateway igw-default":         reasonDefaultNetwork,
		"aws_security_group sg-eks":                reasonEKSClusterSG,
		"aws_vpc_security_group_egress_rule sgr-2": reasonEKSClusterSG,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// With what the listers record, the default network is worked out from
// the listing, without asking AWS again: the VPCs' is_default, the parts'
// vpc_id.
func TestExcludedByDefaultFromTheListing(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{
		harvest.Record("vpc-default", "vpc-default", "aws_vpc", "aws", map[string]string{"is_default": "true"}),
		harvest.Record("vpc-0abc1234", "main", "aws_vpc", "aws", map[string]string{"is_default": "false"}),
		harvest.Record("subnet-default-a", "a", "aws_subnet", "aws", map[string]string{"vpc_id": "vpc-default"}),
		harvest.Record("subnet-0own", "b", "aws_subnet", "aws", map[string]string{"vpc_id": "vpc-0abc1234"}),
		harvest.Record("rtb-default", "main", "aws_route_table", "aws", map[string]string{"vpc_id": "vpc-default", "main": "true"}),
		harvest.Record("rtb-0own", "rtb-0own", "aws_route_table", "aws", map[string]string{"vpc_id": "vpc-0abc1234"}),
		harvest.Record("igw-default", "igw-default", "aws_internet_gateway", "aws", map[string]string{"vpc_id": "vpc-default"}),
		harvest.Record("sg-0own", "web", "aws_security_group", "aws", map[string]string{"group_name": "web"}),
		harvest.Record("sgr-2", "web_egress_all", "aws_vpc_security_group_egress_rule", "aws", map[string]string{"security_group_id": "sg-0own"}),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_vpc vpc-default":              reasonDefaultVPC,
		"aws_subnet subnet-default-a":      reasonDefaultNetwork,
		"aws_route_table rtb-default":      reasonDefaultNetwork,
		"aws_internet_gateway igw-default": reasonDefaultNetwork,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// What the listing leaves open is asked for, and only that: a subnet
// whose VPC wasn't listed takes the default VPCs, not the parts of the
// default network.
func TestExcludedByDefaultAsksForWhatTheListingLeavesOpen(t *testing.T) {
	t.Parallel()
	var ops []string
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeVpcs":
			ops = append(ops, call.Op)
			return defaultNetworkAPI(call)
		case "ListStacks":
			return noStacks
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{
		harvest.Record("subnet-default-a", "a", "aws_subnet", "aws", map[string]string{"vpc_id": "vpc-default"}),
		harvest.Record("subnet-0own", "b", "aws_subnet", "aws", map[string]string{"vpc_id": "vpc-0abc1234"}),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_subnet subnet-default-a": reasonDefaultNetwork,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
	if !reflect.DeepEqual(ops, []string{"DescribeVpcs"}) {
		t.Errorf("asked %v, want the default VPCs only", ops)
	}
}

// Global services have no network to ask about; their stacks are in every
// enabled region.
func TestExcludedByDefaultGlobal(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeRegions":
			return `<DescribeRegionsResponse ` + ec2NS + `><regionInfo><item><regionName>eu-west-2</regionName></item></regionInfo></DescribeRegionsResponse>`
		case "ListStacks":
			return noStacks
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &Provider{region: GlobalPseudoRegion}
	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{
		harvest.RecordBare("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws"),
		harvest.RecordBare("app", "app", "aws_iam_role", "aws"),
	})
	if err != nil || len(excluded) != 0 {
		t.Errorf("got %v, %v; want none: the catalogue leaves out the service-linked role", excluded, err)
	}
}

// What a CloudFormation stack manages is left out, by its ID or the ID
// Terraform imports it with; what no stack manages, or one deleted, isn't.
func TestExcludedByDefaultCloudFormation(t *testing.T) {
	t.Parallel()
	const ns = `xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"`
	var stacks []string
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListStacks":
			if !strings.Contains(call.Body, "StackStatusFilter.member.1=") || strings.Contains(call.Body, "DELETE_COMPLETE&") {
				t.Errorf("ListStacks must ask for live stacks only: %s", call.Body)
			}
			return `<ListStacksResponse ` + ns + `><ListStacksResult><StackSummaries>` +
				`<member><StackId>arn:aws:cloudformation:us-east-1:123456789012:stack/app/1</StackId><StackName>app</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member>` +
				`</StackSummaries></ListStacksResult></ListStacksResponse>`
		case "ListStackResources":
			stacks = append(stacks, call.Body)
			return `<ListStackResourcesResponse ` + ns + `><ListStackResourcesResult><StackResourceSummaries>` +
				`<member><LogicalResourceId>Assets</LogicalResourceId><PhysicalResourceId>app-assets</PhysicalResourceId><ResourceType>AWS::S3::Bucket</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				`<member><LogicalResourceId>Cluster</LogicalResourceId><PhysicalResourceId>apps</PhysicalResourceId><ResourceType>AWS::ECS::Cluster</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				// A rolled-back stack lists what it deleted, which it no longer manages.
				`<member><LogicalResourceId>Old</LogicalResourceId><PhysicalResourceId>own-bucket</PhysicalResourceId><ResourceType>AWS::S3::Bucket</ResourceType><ResourceStatus>DELETE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				`</StackResourceSummaries></ListStackResourcesResult></ListStackResourcesResponse>`
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &Provider{region: "us-east-1"}

	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{
		harvest.RecordBare("app-assets", "assets", "aws_s3_bucket", "aws"),
		// The lister records the cluster's ARN; Terraform imports it by name.
		harvest.RecordBare("arn:aws:ecs:us-east-1:123456789012:cluster/apps", "apps", "aws_ecs_cluster", "aws"),
		harvest.RecordBare("own-bucket", "own", "aws_s3_bucket", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"aws_s3_bucket app-assets": reasonCloudFormation("app"),
		"aws_ecs_cluster arn:aws:ecs:us-east-1:123456789012:cluster/apps": reasonCloudFormation("app"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
	if len(stacks) != 1 || !strings.Contains(stacks[0], "StackName=arn") {
		t.Errorf("ListStackResources: %q, want one call by stack ID", stacks)
	}
}

// What another account shares with this one through AWS RAM is left out:
// that account manages it. What this account owns, or what records no
// owner, stays.
func TestExcludeShared(t *testing.T) {
	t.Parallel()
	calls := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "GetCallerIdentity" {
			t.Errorf("unexpected call %s", call.Op)
			return ""
		}
		calls++
		return recordBudgetsCallerIdentity(call)
	})
	p := &Provider{region: "eu-west-2"}
	resources := []harvest.Resource{
		harvest.Record("subnet-shared", "shared", "aws_subnet", "aws", ownerAttributes(aws.String("210987654321"))),
		harvest.Record("subnet-own", "own", "aws_subnet", "aws", ownerAttributes(aws.String("123456789012"))),
		harvest.Record("vpc-1", "vpc", "aws_vpc", "aws", ownerAttributes(nil)),
	}
	config, err := p.config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excluded := map[string]string{}
	if err := p.excludeShared(ctx, config, resources, excluded); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"aws_subnet subnet-shared": reasonShared("210987654321")}; !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
	if calls != 1 {
		t.Errorf("GetCallerIdentity called %d times, want once", calls)
	}

	// Without an owner recorded, it asks nothing.
	calls = 0
	if err := p.excludeShared(ctx, config, resources[2:], map[string]string{}); err != nil || calls != 0 {
		t.Errorf("without owners: %d calls, %v", calls, err)
	}
}

// The KMS grants AWS services make for themselves, to a service principal
// or a service-linked role, are left out, naming the grantee, without
// asking AWS more than for the stacks; grants to other principals, or that
// record none, stay.
func TestExcludedByDefaultServiceGrants(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	p := &Provider{region: "eu-west-2"}
	const key = "arn:aws:kms:eu-west-2:123456789012:key/k-1:"
	grant := func(id, grantee string) harvest.Resource {
		attributes := map[string]string{}
		if grantee != "" {
			attributes["grantee_principal"] = grantee
		}
		return harvest.Record(key+id, id, "aws_kms_grant", "aws", attributes)
	}
	const linkedRole = "arn:aws:iam::123456789012:role/aws-service-role/rds.amazonaws.com/AWSServiceRoleForRDS"

	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{
		grant("rds", "rds.eu-west-2.amazonaws.com"),
		grant("linked", linkedRole),
		grant("app", "arn:aws:iam::123456789012:role/app"),
		grant("none", ""),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_kms_grant " + key + "rds":    reasonServiceGrant("rds.eu-west-2.amazonaws.com"),
		"aws_kms_grant " + key + "linked": reasonServiceGrant(linkedRole),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// The Auto Scaling groups and launch templates EKS creates for a managed
// node group, which the auto_scaling lister records with their tags, are
// left out, naming the node group, without asking AWS more than for the
// stacks; others stay, a launch template tagged only with the cluster
// included.
func TestExcludedByDefaultEKSNodeGroups(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	p := &Provider{region: "eu-west-2"}
	nodeGroup := map[string]string{"tags.eks:cluster-name": "prod", "tags.eks:nodegroup-name": "workers"}

	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{
		harvest.Record("eks-workers-1a2b", "eks-workers-1a2b", "aws_autoscaling_group", "aws", nodeGroup),
		harvest.Record("lt-eks", "eks-1a2b", "aws_launch_template", "aws", nodeGroup),
		harvest.Record("web", "web", "aws_autoscaling_group", "aws", map[string]string{"tags.Name": "web"}),
		harvest.Record("lt-web", "web", "aws_launch_template", "aws", map[string]string{"tags.eks:cluster-name": "prod"}),
		harvest.Record("arn:aws:eks:eu-west-2:123456789012:nodegroup/prod/workers/1a2b", "workers", "aws_eks_node_group", "aws", nodeGroup),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_autoscaling_group eks-workers-1a2b": reasonEKSNodeGroup("workers"),
		"aws_launch_template lt-eks":             reasonEKSNodeGroup("workers"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// recordStacksAPI answers CloudFormation with one stack, app, that manages
// the bucket app-assets, counting the ListStacks calls.
func recordStacksAPI(t *testing.T, calls *atomic.Int32) func(apiCall) string {
	const ns = `xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"`
	return func(call apiCall) string {
		switch call.Op {
		case "ListStacks":
			calls.Add(1)
			return `<ListStacksResponse ` + ns + `><ListStacksResult><StackSummaries>` +
				`<member><StackId>arn:aws:cloudformation:us-east-1:123456789012:stack/app/1</StackId><StackName>app</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member>` +
				`</StackSummaries></ListStacksResult></ListStacksResponse>`
		case "ListStackResources":
			return `<ListStackResourcesResponse ` + ns + `><ListStackResourcesResult><StackResourceSummaries>` +
				`<member><LogicalResourceId>Assets</LogicalResourceId><PhysicalResourceId>app-assets</PhysicalResourceId><ResourceType>AWS::S3::Bucket</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				`</StackResourceSummaries></ListStackResourcesResult></ListStackResourcesResponse>`
		case "DescribeRegions":
			return `<DescribeRegionsResponse ` + ec2NS + `><regionInfo>` +
				`<item><regionName>eu-west-1</regionName></item><item><regionName>eu-west-2</regionName></item><item><regionName>us-east-1</regionName></item>` +
				`</regionInfo></DescribeRegionsResponse>`
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	}
}

// The global call scans every enabled region's stacks, several at once,
// and merges what they manage.
func TestCloudFormationManagedScansEveryRegion(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	cfg := fakeConfig(recordStacksAPI(t, &calls))
	ctx := fakeRun(t, cfg)
	p := &Provider{region: GlobalPseudoRegion}

	managed, err := p.cloudFormationManaged(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 3 {
		t.Errorf("ListStacks called %d times, want once for each of 3 regions", calls.Load())
	}
	// Each region's stack names the bucket; those of the first region come first.
	bucket := stackResource{stack: "app", cloudFormationType: "AWS::S3::Bucket"}
	if !reflect.DeepEqual(managed, map[string][]stackResource{"app-assets": {bucket, bucket, bucket}}) {
		t.Errorf("managed %v", managed)
	}
}

// A region the run scanned lately with the same credentials isn't scanned
// again, so an import's regional calls reuse what its global call scanned;
// another region, other credentials, or another run, are scanned.
func TestRegionStacksAreScannedOnce(t *testing.T) {
	t.Parallel()
	run := configsOf(ratelimit.New(0))
	var calls atomic.Int32
	config := aws.Config{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(aws.AnonymousCredentials{}),
		HTTPClient:  &fakeAPI{handle: recordStacksAPI(t, &calls)},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}

	for _, region := range []string{"eu-west-1", "eu-west-1", "eu-west-2"} {
		managed, err := run.regionStacks(t.Context(), config, region)
		if err != nil {
			t.Fatal(err)
		}
		if want := []stackResource{{stack: "app", cloudFormationType: "AWS::S3::Bucket"}}; !reflect.DeepEqual(managed["app-assets"], want) {
			t.Errorf("%s: managed %v", region, managed)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("ListStacks called %d times, want once for each of 2 regions", calls.Load())
	}

	other := config
	other.Credentials = aws.NewCredentialsCache(aws.AnonymousCredentials{})
	if _, err := run.regionStacks(t.Context(), other, "eu-west-1"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("other credentials reused another's scan: %d ListStacks calls, want 3", calls.Load())
	}

	if _, err := configsOf(ratelimit.New(0)).regionStacks(t.Context(), config, "eu-west-1"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Errorf("another run reused the run's scan: %d ListStacks calls, want 4", calls.Load())
	}
}
