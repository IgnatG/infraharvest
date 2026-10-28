// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordTargetGroupARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/"

// targetAttachment is a target the alb lister records in the target group
// named group.
func targetAttachment(group, target string) harvest.Resource {
	arn := recordTargetGroupARN + group + "/1"
	return harvest.Record(arn+","+target, group+" "+target, targetAttachmentType, "aws", map[string]string{
		"target_group_arn": arn,
		"target_id":        target,
	})
}

// registeredTargetsAPI answers like a region where the Auto Scaling group
// web registers its instances in the target group asg, through its target
// groups, and in src, through its traffic sources, and the ECS service
// apps/api registers its tasks in ecs, its alternate target group alt and
// its task set's target group set. ecsCalls counts the ECS calls.
func registeredTargetsAPI(t *testing.T, autoScaling string, ecsCalls *int) func(apiCall) string {
	return func(call apiCall) string {
		switch call.Op {
		case "ListStacks":
			return noStacks
		case "DescribeAutoScalingGroups":
			if autoScaling != "" {
				return autoScaling
			}
			return recordQuery(call.Op, recordAutoScalingNS, `<AutoScalingGroups><member>
				<AutoScalingGroupName>web</AutoScalingGroupName>
				<TargetGroupARNs><member>`+recordTargetGroupARN+`asg/1</member></TargetGroupARNs>
				<TrafficSources><member><Identifier>`+recordTargetGroupARN+`src/1</Identifier><Type>elbv2</Type></member></TrafficSources>
				</member></AutoScalingGroups>`)
		case "ListClusters":
			*ecsCalls++
			return `{"clusterArns":["` + recordEcsARN + `cluster/apps"]}`
		case "ListServices":
			*ecsCalls++
			if recordJSONField(call, "cluster") != recordEcsARN+"cluster/apps" {
				t.Errorf("ListServices of %s", call.Body)
			}
			if !strings.Contains(call.Body, `"maxResults":100`) {
				t.Errorf("ListServices without pages of 100: %s", call.Body)
			}
			return `{"serviceArns":["` + recordEcsARN + `service/apps/api"]}`
		case "DescribeServices":
			*ecsCalls++
			return `{"services":[{"serviceName":"api","serviceArn":"` + recordEcsARN + `service/apps/api",
				"loadBalancers":[{"targetGroupArn":"` + recordTargetGroupARN + `ecs/1","advancedConfiguration":{"alternateTargetGroupArn":"` + recordTargetGroupARN + `alt/1"}}],
				"taskSets":[{"loadBalancers":[{"targetGroupArn":"` + recordTargetGroupARN + `set/1"}]}]}]}`
		}
		return recordUnexpected(t, call)
	}
}

// The targets an Auto Scaling group or an ECS service registers are left
// out, with the owner in the reason; the targets of a target group no one
// else registers in, and the target groups themselves, stay.
func TestExcludedByDefaultRegisteredTargets(t *testing.T) {
	t.Parallel()
	ecsCalls := 0
	ctx := useFakeAPI(t, registeredTargetsAPI(t, "", &ecsCalls))
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{
		harvest.RecordBare(recordTargetGroupARN+"asg/1", "asg", "aws_lb_target_group", "aws"),
		targetAttachment("asg", "i-1"),
		targetAttachment("src", "i-2"),
		targetAttachment("ecs", "10.0.0.1"),
		targetAttachment("alt", "10.0.0.2"),
		targetAttachment("set", "10.0.0.3"),
		targetAttachment("own", "i-3"),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	asg := reasonRegisteredTarget("Auto Scaling group web")
	service := reasonRegisteredTarget("ECS service apps/api")
	want := map[string]string{
		targetAttachmentType + " " + resources[1].Observed.ID: asg,
		targetAttachmentType + " " + resources[2].Observed.ID: asg,
		targetAttachmentType + " " + resources[3].Observed.ID: service,
		targetAttachmentType + " " + resources[4].Observed.ID: service,
		targetAttachmentType + " " + resources[5].Observed.ID: service,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
	if ecsCalls != 3 {
		t.Errorf("%d ECS calls, want 3: one each to list clusters, list services and describe them", ecsCalls)
	}
}

// When Auto Scaling groups register in every listed target group, ECS is
// not asked; when no target is listed, neither is.
func TestExcludedByDefaultRegisteredTargetsAsksOnlyWhatItNeeds(t *testing.T) {
	t.Parallel()
	ecsCalls := 0
	ctx := useFakeAPI(t, registeredTargetsAPI(t, "", &ecsCalls))
	p := &Provider{region: "us-east-1"}

	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{targetAttachment("asg", "i-1")})
	if err != nil || len(excluded) != 1 || ecsCalls != 0 {
		t.Errorf("got %v, %v after %d ECS calls; want the target left out without asking ECS", excluded, err, ecsCalls)
	}

	ctx = useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	excluded, err = p.ExcludedByDefault(ctx, []harvest.Resource{
		harvest.RecordBare(recordTargetGroupARN+"asg/1", "asg", "aws_lb_target_group", "aws"),
	})
	if err != nil || len(excluded) != 0 {
		t.Errorf("without targets: got %v, %v; want none", excluded, err)
	}
}

// The targets of a target group the AWS Load Balancer Controller of an EKS
// cluster created, told by the tag it gives them, are left out without a
// call: when it created every listed target group, neither Auto Scaling
// nor ECS is asked.
func TestExcludedByDefaultRegisteredTargetsOfLoadBalancerController(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{
		harvest.Record(recordTargetGroupARN+"k8s/1", "k8s", "aws_lb_target_group", "aws", map[string]string{loadBalancerControllerTag: "apps"}),
		targetAttachment("k8s", "10.0.0.1"),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		targetAttachmentType + " " + resources[1].Observed.ID: reasonRegisteredTarget("the AWS Load Balancer Controller of EKS cluster apps"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// An Auto Scaling error is returned, and what ECS registers is still left
// out.
func TestExcludedByDefaultRegisteredTargetsReturnsErrors(t *testing.T) {
	t.Parallel()
	ecsCalls := 0
	ctx := useFakeAPI(t, registeredTargetsAPI(t, recordQueryError(recordAutoScalingNS, "AccessDenied"), &ecsCalls))
	p := &Provider{region: "us-east-1"}
	resources := []harvest.Resource{targetAttachment("asg", "i-1"), targetAttachment("ecs", "10.0.0.1")}

	excluded, err := p.ExcludedByDefault(ctx, resources)

	if err == nil || !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "Auto Scaling") {
		t.Errorf("want the Auto Scaling error returned, got %v", err)
	}
	want := map[string]string{
		targetAttachmentType + " " + resources[1].Observed.ID: reasonRegisteredTarget("ECS service apps/api"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// The target groups the listed Auto Scaling groups and ECS services
// recorded are claimed without a call: when they claim every listed target
// group, neither Auto Scaling nor ECS is asked.
func TestExcludedByDefaultRegisteredTargetsFromListed(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	p := &Provider{region: "us-east-1"}
	group := map[string]string{}
	recordTargetGroups(group, []string{recordTargetGroupARN + "asg/1", ""})
	service := map[string]string{"cluster": "apps", "name": "api"}
	recordTargetGroups(service, []string{recordTargetGroupARN + "ecs/1", recordTargetGroupARN + "alt/1"})
	resources := []harvest.Resource{
		harvest.Record("web", "web", "aws_autoscaling_group", "aws", group),
		harvest.Record(recordEcsARN+"service/apps/api", "apps_api", "aws_ecs_service", "aws", service),
		targetAttachment("asg", "i-1"),
		targetAttachment("ecs", "10.0.0.1"),
		targetAttachment("alt", "10.0.0.2"),
	}

	excluded, err := p.ExcludedByDefault(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		targetAttachmentType + " " + resources[2].Observed.ID: reasonRegisteredTarget("Auto Scaling group web"),
		targetAttachmentType + " " + resources[3].Observed.ID: reasonRegisteredTarget("ECS service apps/api"),
		targetAttachmentType + " " + resources[4].Observed.ID: reasonRegisteredTarget("ECS service apps/api"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// What recordTargetGroups records, recordedTargetGroups reads back, in
// order and without the empty ones.
func TestRecordedTargetGroups(t *testing.T) {
	t.Parallel()
	attributes := map[string]string{"name": "api"}
	recordTargetGroups(attributes, []string{"a", "", "b"})
	if got := recordedTargetGroups(attributes); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
	if got := recordedTargetGroups(map[string]string{}); got != nil {
		t.Errorf("none recorded: got %v", got)
	}
}
