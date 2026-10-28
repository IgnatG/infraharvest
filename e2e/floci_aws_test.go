// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// createDefaultGroupRules gives the default security group of each VPC
// in the emulator account accessKey names, in region, the inbound rule
// AWS gives it and Floci doesn't: all traffic from the group itself. The
// default selection leaves out a default group only while it is as AWS
// creates it, so without the rule each would be imported as one
// configuration has changed, which no AWS account would show. The rule
// is the cloud's, not the fixture's, so the test's state doesn't hold it.
func createDefaultGroupRules(ctx context.Context, t *testing.T, accessKey, region string) {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	svc := ec2.NewFromConfig(cfg)
	groups, err := svc.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{Name: awssdk.String("group-name"), Values: []string{"default"}}},
	})
	if err != nil {
		t.Fatalf("list the default security groups: %v", err)
	}
	for _, group := range groups.SecurityGroups {
		if fromItself(group) {
			continue
		}
		if _, err := svc.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: group.GroupId,
			IpPermissions: []ec2types.IpPermission{{
				IpProtocol:       awssdk.String("-1"),
				UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: group.GroupId}},
			}},
		}); err != nil {
			t.Fatalf("give the default security group %s its inbound rule: %v", awssdk.ToString(group.GroupId), err)
		}
	}
}

// fromItself reports whether group already allows all inbound traffic
// from itself, as a later seeding of the same emulator finds it.
func fromItself(group ec2types.SecurityGroup) bool {
	for _, permission := range group.IpPermissions {
		if awssdk.ToString(permission.IpProtocol) != "-1" {
			continue
		}
		for _, pair := range permission.UserIdGroupPairs {
			if awssdk.ToString(pair.GroupId) == awssdk.ToString(group.GroupId) {
				return true
			}
		}
	}
	return false
}
