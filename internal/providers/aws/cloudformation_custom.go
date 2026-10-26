// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// customResourceRule is what infraharvest knows of a custom resource type
// the AWS CDK creates for its constructs: what it does, and what to do
// about it before the stack is deleted.
type customResourceRule struct {
	// advice says what the resource does, and what to do instead or first:
	// a format of the resource it acts on, then its logical ID.
	advice string
	// target is the property that names the resource it acts on, such as a
	// bucket's name; without it, or where the template doesn't tell, its
	// physical ID stands in.
	target string
	// mustRemove says deleting it, as deleting the stack does unless it is
	// retained, destroys what the import brought under Terraform, such as
	// a bucket's objects: it must be gone from the stack before the stack
	// is deleted, and --cfn-ignore doesn't acknowledge it.
	mustRemove bool
}

// cdkCustomResources are the custom resource types of the AWS CDK's own
// constructs, by type, from the providers in aws-cdk-lib: each handler
// acts on resources the stack, and now Terraform, has, and some undo or
// destroy what they did when CloudFormation deletes them.
var cdkCustomResources = map[string]customResourceRule{
	"Custom::S3AutoDeleteObjects": {mustRemove: true, target: "BucketName", advice: "the CDK's autoDeleteObjects: when CloudFormation deletes it, its handler deletes every object of bucket %s, retained or not. " +
		"Before deleting the stack, update it with the copy of its template that retains it, so that CloudFormation no longer calls its handler; then take it out of the stack, with autoDeleteObjects: false in the CDK app or by removing %s and its provider from the template, and update the stack again; and remove the bucket's aws-cdk:auto-delete-objects tag, which the handler acts on"},
	"Custom::ECRAutoDeleteImages": {mustRemove: true, target: "RepositoryName", advice: "the CDK's emptyOnDelete (autoDeleteImages): when CloudFormation deletes it, its handler deletes every image of repository %s, retained or not. " +
		"Before deleting the stack, update it with the copy of its template that retains it, so that CloudFormation no longer calls its handler; then take it out of the stack, with emptyOnDelete: false in the CDK app or by removing %s and its provider from the template, and update the stack again; and remove the repository's aws-cdk:auto-delete-images tag, which the handler acts on"},
	"Custom::SyntheticsAutoDeleteUnderlyingResources": {mustRemove: true, advice: "the CDK's cleanup of a Synthetics canary: when CloudFormation deletes it, its handler deletes the Lambda function and layers of the canary it cleans up (%s). " +
		"Before deleting the stack, update it with the copy of its template that retains it; then take it out of the stack, with cleanup: Cleanup.NOTHING in the CDK app or by removing %s from the template, and update the stack again"},
	"Custom::LogRetention": {target: "LogGroupName", advice: "the CDK's logRetention: it sets the retention of log group %s, which Lambda or the CDK creates, and, with a removal policy of destroy, deletes the log group when CloudFormation deletes it. " +
		"Import the log group itself, with its retention_in_days, then acknowledge %s with --cfn-ignore"},
	"Custom::CDKBucketDeployment": {advice: "the CDK's BucketDeployment: it copies files into a bucket, and, with retainOnDelete: false, deletes them when CloudFormation deletes it (%s). " +
		"Terraform imports the bucket, not the files: deploy them with your pipeline or aws_s3_object, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWS": {advice: "the CDK's AwsCustomResource: it makes the AWS API calls of its onCreate, onUpdate and onDelete, and runs onDelete when CloudFormation deletes it (%s). " +
		"Find what its calls set up, manage that in Terraform where it can, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::VpcRestrictDefaultSG": {advice: "the CDK's restrictDefaultSecurityGroup: it removes the rules of the default security group of a VPC, and adds them back when CloudFormation deletes it (%s). " +
		"Manage the default group's rules with aws_default_security_group, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWSCDKOpenIdConnectProvider": {advice: "the CDK's iam.OpenIdConnectProvider: it creates the IAM OIDC provider %s, and deletes it when CloudFormation deletes it. " +
		"Import it as aws_iam_openid_connect_provider by that ARN, which infraharvest doesn't list, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::S3BucketNotifications": {advice: "the CDK's bucket notifications: it sets a bucket's notification configuration, and clears it when CloudFormation deletes it (%s). " +
		"Import the bucket's aws_s3_bucket_notification, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::CrossRegionExportWriter": {advice: "the CDK's crossRegionReferences: it writes SSM parameters another region's stacks read (%s), and deletes them when CloudFormation deletes it. " +
		"Replace the references with Terraform's own (outputs, remote state or data sources), then acknowledge %s with --cfn-ignore"},
	"Custom::CrossRegionExportReader": {advice: "the CDK's crossRegionReferences: it reads the SSM parameters another region's stack writes (%s). " +
		"Replace the references with Terraform's own (outputs, remote state or data sources), then acknowledge %s with --cfn-ignore"},
	"Custom::CrossAccountZoneDelegation": {advice: "the CDK's CrossAccountZoneDelegationRecord: it writes the NS record of a zone into its parent zone in another account (%s), and deletes it when CloudFormation deletes it. " +
		"Import the record into the parent account's Terraform, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::DeleteExistingRecordSet": {advice: "the CDK's deleteExisting of a Route 53 record: it deleted the record set the stack's record replaced when it was created (%s), and does nothing more. " +
		"Acknowledge %s with --cfn-ignore"},
	"Custom::Trigger": {advice: "the CDK's Trigger: it ran a Lambda function when the stack was deployed (%s). " +
		"Run that function from your pipeline if it is still needed, and acknowledge %s with --cfn-ignore"},
	"Custom::AWSCDK-EKS-FargateProfile": {advice: "the CDK's Fargate profile of an eks.Cluster (%s), which it deletes when CloudFormation deletes it. " +
		"Import it as aws_eks_fargate_profile, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWSCDK-EKS-KubernetesResource": {advice: "the CDK's Kubernetes manifest (%s): kubectl applies it, and deletes it from the cluster when CloudFormation deletes it. " +
		"Manage it with the kubernetes provider or your cluster's own tooling, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWSCDK-EKS-KubernetesPatch": {advice: "the CDK's patch of a Kubernetes resource (%s), which it undoes when CloudFormation deletes it. " +
		"Manage it with the kubernetes provider or your cluster's own tooling, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWSCDK-EKS-HelmChart": {advice: "the CDK's Helm chart (%s), which it uninstalls when CloudFormation deletes it. " +
		"Manage it with the helm provider or your cluster's own tooling, and acknowledge %s with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its handler"},
	"Custom::AWSCDK-EKS-KubernetesObjectValue": {advice: "the CDK's read of a value from a Kubernetes object (%s), which changes nothing. " +
		"Acknowledge %s with --cfn-ignore"},
}

// genericCustomResource is why Terraform can't import a custom resource
// infraharvest knows nothing more about.
const genericCustomResource = "a custom resource, which CloudFormation creates, updates and deletes through its provider, a Lambda function or an SNS topic: Terraform has no resource for it, so decide what takes its place, and acknowledge it with --cfn-ignore; the copy of the template with Retain added keeps CloudFormation from calling its provider when the stack is deleted"

// isCustomResource reports whether cfnType is a custom resource's.
func isCustomResource(cfnType string) bool {
	return strings.HasPrefix(cfnType, "Custom::") || cfnType == "AWS::CloudFormation::CustomResource"
}

// customResourceAdvice returns why Terraform can't import the custom
// resource r of stack, with what to do about it, and whether it must be
// taken out of the stack before the stack is deleted (see
// customResourceRule). The resource it acts on is named from its
// template's properties where the rule says which and the template tells,
// a Ref resolved to the physical ID of the stack's resource; else by its
// own physical ID.
func customResourceAdvice(stack *scopedStack, r *scopedResource) (string, bool) {
	rule, ok := cdkCustomResources[r.cfnType]
	if !ok {
		return genericCustomResource, false
	}
	target := r.physicalID
	if rule.target != "" {
		if name := stack.propertyValue(r.logicalID, rule.target); name != "" {
			target = name
		}
	}
	return fmt.Sprintf(rule.advice, target, r.logicalID), rule.mustRemove
}

// propertyValue returns the value of the property of the stack's resource
// logicalID, as a literal, or as a Ref, in either form, to a resource of
// the stack, whose physical ID it returns; "" for anything else.
func (s *scopedStack) propertyValue(logicalID, property string) string {
	node := mappingValue(mappingValue(s.template.resource(logicalID), "Properties"), property)
	if node == nil {
		return ""
	}
	ref := ""
	switch {
	case node.Kind == yaml.ScalarNode && node.Tag == "!Ref":
		ref = node.Value
	case node.Kind == yaml.ScalarNode && !strings.HasPrefix(node.Tag, "!"):
		return node.Value
	default:
		if r := mappingValue(node, "Ref"); r != nil && r.Kind == yaml.ScalarNode {
			ref = r.Value
		}
	}
	for _, other := range s.resources {
		if ref != "" && other.logicalID == ref {
			return other.physicalID
		}
	}
	return ""
}

// Acknowledge marks the resources ignore names, as --cfn-ignore gives
// them, as acknowledged: by logical ID, or as <stack>/<logical ID> where
// several stacks of the scope have the logical ID. Only resources
// Terraform can't import may be acknowledged, and not those that must be
// taken out of the stack first (see customResourceRule.mustRemove).
func (s *StackScope) Acknowledge(ignore []string) error {
	for _, entry := range ignore {
		stackName, logicalID, qualified := strings.Cut(entry, "/")
		if !qualified {
			stackName, logicalID = "", entry
		}
		var found []*scopedResource
		var stacks []string
		for _, stack := range s.stacks {
			if qualified && stack.name != stackName {
				continue
			}
			for _, r := range stack.resources {
				if r.logicalID == logicalID {
					found = append(found, r)
					stacks = append(stacks, stack.name)
				}
			}
		}
		switch {
		case len(found) == 0:
			return fmt.Errorf("--cfn-ignore %s: no resource of the stacks has that logical ID", entry)
		case len(found) > 1:
			slices.Sort(stacks)
			return fmt.Errorf("--cfn-ignore %s: stacks %s each have it: name one, as <stack>/%s", entry, strings.Join(stacks, ", "), logicalID)
		}
		r := found[0]
		switch {
		case r.mustRemove:
			return fmt.Errorf("--cfn-ignore %s: deleting it destroys what the stack's resources hold, so it can't be acknowledged: %s", entry, r.notImportable)
		case r.outcome != "" || r.notImportable == "":
			return fmt.Errorf("--cfn-ignore %s: only a resource Terraform can't import, such as a custom resource, can be acknowledged", entry)
		}
		r.acknowledged = true
	}
	return nil
}
