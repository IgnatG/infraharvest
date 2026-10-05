// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

func TestEbsReturnsDescribeInstancesError(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		const ns = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`
		switch call.Op {
		case "DescribeVolumes":
			return `<DescribeVolumesResponse ` + ns + `><volumeSet><item><volumeId>vol-1</volumeId>
				<attachmentSet><item><volumeId>vol-1</volumeId><instanceId>i-1</instanceId>
				<device>/dev/sda1</device><status>attached</status></item></attachmentSet>
				</item></volumeSet></DescribeVolumesResponse>`
		case "DescribeInstances":
			return `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>not allowed</Message></Error></Errors></Response>`
		}
		t.Fatalf("unexpected call %s", call.Op)
		return ""
	})
	g := &EbsGenerator{}

	// Before the fix, the ignored error left the response nil and this panicked.
	err := g.InitResources()

	if err == nil || !strings.Contains(err.Error(), "i-1") {
		t.Errorf("want the DescribeInstances error naming the instance, got %v", err)
	}
}

// Reading a SecureString parameter decrypts it, which the read-only role
// cannot do, so the lister leaves those out.
func TestSsmSkipsSecureStrings(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeParameters" {
			t.Fatalf("unexpected call %s", call.Op)
		}
		return `{"Parameters":[{"Name":"/app/endpoint","Type":"String"},{"Name":"/app/password","Type":"SecureString"},{"Name":"/app/hosts","Type":"StringList"}]}`
	})
	g := &SsmGenerator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_ssm_parameter", "/app/endpoint", "/app/hosts")
}

// serveWafv2Associations answers the WAFv2 listers with one web ACL whose
// association listing returns associations for load balancers and
// associationError for every other resource type.
func serveWafv2Associations(associationError string) func(apiCall) string {
	return func(call apiCall) string {
		switch call.Op {
		case "ListWebACLs":
			return `{"WebACLs":[{"Id":"12345678-acl","Name":"web","ARN":"arn:acl"}]}`
		case "ListResourcesForWebACL":
			if strings.Contains(call.Body, `"ResourceType":"APPLICATION_LOAD_BALANCER"`) {
				return `{"ResourceArns":["arn:alb"]}`
			}
			return associationError
		}
		return `{}`
	}
}

// A region that lacks a resource type rejects ListResourcesForWebACL for
// it; that must not hide the associations of the other types.
func TestWafv2SkipsResourceTypesTheRegionLacks(t *testing.T) {
	useFakeAPI(t, serveWafv2Associations(`{"__type":"WAFInvalidParameterException","message":"resource type not supported in this region"}`))
	g := NewWafv2RegionalGenerator()

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_wafv2_web_acl_association", "arn:alb")
}

func TestWafv2ReportsOtherAssociationErrors(t *testing.T) {
	useFakeAPI(t, serveWafv2Associations(`{"__type":"AccessDeniedException","message":"not allowed"}`))
	g := NewWafv2RegionalGenerator()

	if err := g.InitResources(); err == nil {
		t.Error("want the ListResourcesForWebACL error")
	}
}
