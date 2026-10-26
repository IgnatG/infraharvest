// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/xml"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

const (
	nestedParent = "arn:aws:cloudformation:eu-west-2:123456789012:stack/shop/1"
	nestedKept   = "arn:aws:cloudformation:eu-west-2:123456789012:stack/shop-Queues-1ABC/2"
	nestedGone   = "arn:aws:cloudformation:eu-west-2:123456789012:stack/shop-Tables-2DEF/3"
)

// nestedParentTemplate is the template of shop, as a deleted stack keeps
// it: JSON, with a description, parameters, outputs and the stack's
// resources, the bucket it retained and its two nested stacks, one of
// them retained.
const nestedParentTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Description": "The shop's storage",
  "Parameters": {"Env": {"Type": "String", "Default": "prod"}},
  "Resources": {
    "Assets": {
      "Type": "AWS::S3::Bucket",
      "DeletionPolicy": "Retain",
      "UpdateReplacePolicy": "Retain",
      "Properties": {"BucketName": {"Fn::Sub": "shop-${Env}-assets"}}
    },
    "Queues": {
      "Type": "AWS::CloudFormation::Stack",
      "DeletionPolicy": "Retain",
      "Properties": {"TemplateURL": "https://example.com/queues.json"}
    },
    "Tables": {
      "Type": "AWS::CloudFormation::Stack",
      "Properties": {"TemplateURL": "https://example.com/tables.json"}
    }
  },
  "Outputs": {"AssetsBucket": {"Value": {"Ref": "Assets"}}}
}`

// nestedStacksAPI answers with shop, deleted: it retained its bucket and
// its nested stack Queues, which is live and has a queue that isn't
// retained, and deleted its nested stack Tables, which retained its table.
// It counts the ListStackResources calls by stack.
func nestedStacksAPI(t *testing.T, listed map[string]int) func(apiCall) string {
	member := func(logical, physical, typ, status string) string {
		return `<member><LogicalResourceId>` + logical + `</LogicalResourceId><PhysicalResourceId>` + physical + `</PhysicalResourceId><ResourceType>` + typ +
			`</ResourceType><ResourceStatus>` + status + `</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>`
	}
	described := func(id, name, status, parent string) string {
		p := ""
		if parent != "" {
			p = `<ParentId>` + parent + `</ParentId>`
		}
		return `<Stacks><member><StackId>` + id + `</StackId><StackName>` + name + `</StackName><StackStatus>` + status + `</StackStatus>` + p + `<CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`
	}
	return func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		stack := form.Get("StackName")
		switch call.Op {
		case "DescribeStacks":
			switch stack {
			case nestedParent:
				return recordQuery(call.Op, recordCfnNS, described(nestedParent, "shop", "DELETE_COMPLETE", ""))
			case nestedKept:
				// Retained, so live, and no longer anyone's: CloudFormation
				// keeps its parent's ID.
				return recordQuery(call.Op, recordCfnNS, described(nestedKept, "shop-Queues-1ABC", "UPDATE_COMPLETE", nestedParent))
			case nestedGone:
				return recordQuery(call.Op, recordCfnNS, described(nestedGone, "shop-Tables-2DEF", "DELETE_COMPLETE", nestedParent))
			}
			return recordQueryError(recordCfnNS, "ValidationError")
		case "GetTemplate":
			body := map[string]string{
				nestedParent: nestedParentTemplate,
				nestedKept:   "Resources:\n  Orders:\n    Type: AWS::SQS::Queue\n",
				nestedGone:   "Resources:\n  Carts:\n    Type: AWS::DynamoDB::Table\n    DeletionPolicy: Retain\n",
			}[stack]
			var b strings.Builder
			_ = xml.EscapeText(&b, []byte(body))
			return recordQuery(call.Op, recordCfnNS, `<TemplateBody>`+b.String()+`</TemplateBody>`)
		case "ListStackResources":
			listed[stack]++
			switch stack {
			case nestedParent:
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+
					member("Assets", "shop-prod-assets", "AWS::S3::Bucket", "DELETE_SKIPPED")+
					member("Queues", nestedKept, "AWS::CloudFormation::Stack", "DELETE_SKIPPED")+
					member("Tables", nestedGone, "AWS::CloudFormation::Stack", "DELETE_COMPLETE")+
					`</StackResourceSummaries>`)
			case nestedKept:
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+member("Orders", "https://sqs.eu-west-2.amazonaws.com/123456789012/orders", "AWS::SQS::Queue", "CREATE_COMPLETE")+`</StackResourceSummaries>`)
			case nestedGone:
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+member("Carts", "carts", "AWS::DynamoDB::Table", "DELETE_SKIPPED")+`</StackResourceSummaries>`)
			}
		}
		return recordUnexpected(t, call)
	}
}

// A deleted stack's nested stacks are read with it: the one it retained
// (DELETE_SKIPPED) is live, as its own status says, so its resources are
// checked for Retain; the one it deleted is deleted, and what it retained
// is imported. A nested stack named before its parent is read once, so its
// resources are in the scope once, and --cfn-ignore finds them.
func TestResolveStacksNestedUnderDeletedParent(t *testing.T) {
	listed := map[string]int{}
	useFakeAPI(t, nestedStacksAPI(t, listed))

	scope, err := ResolveStacks(t.Context(), "default", "", "eu-west-2", []string{nestedKept, nestedParent}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]*scopedStack{}
	var names []string
	for _, s := range scope.stacks {
		byName[s.name] = s
		names = append(names, s.name)
	}
	if want := []string{"shop-Queues-1ABC", "shop", "shop-Tables-2DEF"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("stacks %v, want %v", names, want)
	}
	if want := map[string]int{nestedKept: 1, nestedParent: 1, nestedGone: 1}; !reflect.DeepEqual(listed, want) {
		t.Errorf("ListStackResources calls %v, want one a stack", listed)
	}
	kept := byName["shop-Queues-1ABC"]
	if kept.deleted || kept.status != "UPDATE_COMPLETE" || kept.resources[0].retain == nil {
		t.Errorf("the retained nested stack: deleted %v, status %s, Orders retain %v", kept.deleted, kept.status, kept.resources[0].retain)
	}
	gone := byName["shop-Tables-2DEF"]
	if !gone.deleted || gone.status != "DELETE_COMPLETE" || gone.parent != nestedParent || gone.resources[0].retain != nil {
		t.Errorf("the deleted nested stack: %+v", gone)
	}
	if shop := byName["shop"]; !shop.deleted || len(shop.resources) != 3 || shop.resources[0].retain != nil {
		t.Errorf("shop: %+v", shop)
	}
	if err := scope.Acknowledge([]string{"Orders"}); err == nil || !strings.Contains(err.Error(), "only a resource Terraform can't import") {
		t.Errorf("Orders, found once: %v", err)
	}
}

// A nested stack that can't be read, as one the role may not describe, is
// in the scope as unreadable, with the reason, and the rest is read; the
// report then keeps it, and the stack it is nested in, from being safe to
// delete.
func TestResolveStacksUnreadableNested(t *testing.T) {
	api := scopeStacksAPI(t)
	useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		if form.Get("StackName") != scopeNetworkStack {
			return api(call)
		}
		if call.Op != "DescribeStacks" {
			return recordUnexpected(t, call)
		}
		return recordQueryError(recordCfnNS, "AccessDenied")
	})

	scope, err := ResolveStacks(t.Context(), "default", "", "eu-west-2", []string{"app"}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}

	var network *scopedStack
	for _, s := range scope.stacks {
		if s.id == scopeNetworkStack {
			network = s
		}
	}
	if network == nil || !strings.HasPrefix(network.unreadable, "AccessDenied: ") || network.parent != scopeAppStack || network.resources != nil || network.template != nil {
		t.Fatalf("the nested stack: %+v", network)
	}
	if summary := strings.Join(scope.Summary(), "\n"); !strings.Contains(summary, "app-Network-1ABC (eu-west-2), nested in "+scopeAppStack+": couldn't be read") {
		t.Errorf("summary:\n%s", summary)
	}

	r := &report.Report{}
	var stacks []report.CloudFormationStack
	for _, s := range scope.stacks {
		stacks = append(stacks, s.report(nil))
	}
	r.AddCloudFormationStacks(stacks)
	r.Finish(nil, nil, false)
	for _, s := range r.CloudFormationStacks {
		blockers := strings.Join(s.Blockers, "\n")
		want := "it couldn't be read, so what it holds, which deleting it may delete, is unknown: AccessDenied: "
		if s.ID == scopeAppStack {
			want = "in nested stack app-Network-1ABC: " + want
		}
		if s.SafeToDelete || !strings.Contains(blockers, want) {
			t.Errorf("%s: safe to delete %v, blockers:\n%s", s.Name, s.SafeToDelete, blockers)
		}
	}
	if md := r.Markdown(); !strings.Contains(md, "`"+scopeNetworkStack+"`, unreadable, nested in") {
		t.Errorf("markdown:\n%s", md)
	}
}
