// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordACMCertificate = "arn:aws:acm:us-east-1:123456789012:certificate/"

// The lister lists every certificate, across pages and of every key type,
// by ARN, named after its UUID and domain, with its domain, status and
// type: the catalogue leaves out by status those that will never be in
// use again.
func TestRecordACMListsCertificates(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListCertificates" {
			return recordUnexpected(t, call)
		}
		if !strings.Contains(call.Body, `"keyTypes"`) && !strings.Contains(call.Body, `"KeyTypes"`) {
			t.Errorf("certificates not asked for of every key type: %s", call.Body)
		}
		if recordSecondPage(call) {
			return `{"CertificateSummaryList":[{"CertificateArn":"` + recordACMCertificate + `c-2","DomainName":"old.example.com","Status":"EXPIRED","Type":"IMPORTED"}]}`
		}
		return `{"CertificateSummaryList":[{"CertificateArn":"` + recordACMCertificate + `c-1","DomainName":"www.example.com.","Status":"ISSUED","Type":"AMAZON_ISSUED"},
			{"CertificateArn":"` + recordACMCertificate + `c-3","DomainName":"api.example.com"}],"NextToken":"` + nextPageToken + `"}`
	})
	g := &ACMLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_acm_certificate", recordACMCertificate + "c-1", "c-1_www.example.com", map[string]string{
			"domain_name": "www.example.com.", "status": "ISSUED", "type": "AMAZON_ISSUED"}},
		// An answer without a status or a type records none.
		recordedResource{"aws_acm_certificate", recordACMCertificate + "c-3", "c-3_api.example.com", map[string]string{"domain_name": "api.example.com"}},
		recordedResource{"aws_acm_certificate", recordACMCertificate + "c-2", "c-2_old.example.com", map[string]string{
			"domain_name": "old.example.com", "status": "EXPIRED", "type": "IMPORTED"}},
	)
}

func TestRecordACMReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &ACMLister{}

	assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
}
