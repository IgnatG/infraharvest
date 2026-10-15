// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

const (
	recordWafRegionalALB   = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/shop/50dc6c495c0c9188"
	recordWafRegionalStage = "arn:aws:apigateway:us-east-1::/restapis/a1b2c3/stages/prod"
)

// recordWafRegionalAPI answers as recordWafAPI does, and answers
// ListResourcesForWebACL from associations, by web ACL ID and resource
// type. A pair missing there is unexpected.
func recordWafRegionalAPI(t *testing.T, associations map[[2]string]string) func(apiCall) string {
	lists := recordWafAPI(t)
	return func(call apiCall) string {
		if call.Op != "ListResourcesForWebACL" {
			return lists(call)
		}
		if answer, ok := associations[[2]string{recordJSONField(call, "WebACLId"), recordJSONField(call, "ResourceType")}]; ok {
			return answer
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists the same twelve types as the WAF lister, in the same
// order and the same way, from WAF Regional and as aws_wafregional_ types.
// Then it lists the resources each web ACL protects, asking for load
// balancers and API Gateway stages in turn: each association is listed by
// <web ACL ID>:<resource ARN>, named by the web ACL's name and the
// resource's. A type the region rejects as an invalid parameter, or a web
// ACL gone since it was listed, is skipped.
func TestRecordWafRegionalListsEveryType(t *testing.T) {
	mainID, backupID := recordWafID(0, 1), recordWafID(0, 2)
	useFakeAPI(t, recordWafRegionalAPI(t, map[[2]string]string{
		{mainID, "APPLICATION_LOAD_BALANCER"}:   `{"ResourceArns":["` + recordWafRegionalALB + `"]}`,
		{mainID, "API_GATEWAY"}:                 `{"ResourceArns":["` + recordWafRegionalStage + `"]}`,
		{backupID, "APPLICATION_LOAD_BALANCER"}: recordJSONError("WAFInvalidParameterException"),
		{backupID, "API_GATEWAY"}:               recordJSONError("WAFNonexistentItemException"),
	}))
	g := &WafRegionalLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	association := func(resourceARN, name string) recordedResource {
		return recordedResource{
			"aws_wafregional_web_acl_association",
			mainID + ":" + resourceARN,
			"main_" + mainID[:8] + "_" + name,
			map[string]string{"resource_arn": resourceARN, "web_acl_id": mainID},
		}
	}
	want := recordedWaf("aws_wafregional_")
	want = append(want,
		association(recordWafRegionalALB, "shop"),
		association(recordWafRegionalStage, "prod"),
	)
	assertRecorded(t, g.Resources, want...)
}

// An error from any list operation is returned, and the operations after it
// are not called.
func TestRecordWafRegionalReturnsErrors(t *testing.T) {
	for _, o := range recordWafOps {
		t.Run(o.op, func(t *testing.T) {
			useFakeAPI(t, recordWafErrorAPI(t, o.op))
			g := &WafRegionalLister{}

			assertRecordedError(t, g.List(), "WAFInternalErrorException")
		})
	}
}

// An error listing a web ACL's resources, other than the two that skip a
// resource type, is returned.
func TestRecordWafRegionalReturnsAssociationErrors(t *testing.T) {
	mainID, backupID := recordWafID(0, 1), recordWafID(0, 2)
	useFakeAPI(t, recordWafRegionalAPI(t, map[[2]string]string{
		{mainID, "APPLICATION_LOAD_BALANCER"}:   `{"ResourceArns":[]}`,
		{mainID, "API_GATEWAY"}:                 recordJSONError("WAFInternalErrorException"),
		{backupID, "APPLICATION_LOAD_BALANCER"}: fmt.Sprintf(`{"ResourceArns":[%q]}`, recordWafRegionalALB),
		{backupID, "API_GATEWAY"}:               `{"ResourceArns":[]}`,
	}))
	g := &WafRegionalLister{}

	assertRecordedError(t, g.List(), "WAFInternalErrorException")
}

// A page that sends back the marker it was asked with is an error, not an
// endless listing.
func TestRecordWafRegionalReturnsRepeatedMarkerError(t *testing.T) {
	useFakeAPI(t, recordWafRepeatedMarkerAPI(t))
	g := &WafRegionalLister{}

	assertRecordedError(t, g.List(), "pagination did not advance")
}

// As in the WAF lister, an ID of eight characters or fewer ends the name
// whole.
func TestRecordWafRegionalNamesShortIDs(t *testing.T) {
	useFakeAPI(t, recordWafShortIDAPI(t))
	g := &WafRegionalLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_wafregional_web_acl", "short", "main_short", nil},
		recordedResource{"aws_wafregional_web_acl", "abcdefgh", "edge_abcdefgh", nil},
	)
}
