// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Helpers for the *_record_test.go files, which record what each AWS lister
// lists so that a rewrite of a lister can show it lists the same.

// recordedResource is what a lister recorded about one resource: its
// Terraform type, its ID, its raw name and its attributes. A nil and an
// empty attribute map are the same.
type recordedResource struct {
	Type  string
	ID    string
	Name  string
	Attrs map[string]string
}

func (r recordedResource) String() string {
	return fmt.Sprintf("%s %q name=%q attrs=%v", r.Type, r.ID, r.Name, r.Attrs)
}

func recordedResources(resources []harvest.Resource) []recordedResource {
	got := make([]recordedResource, 0, len(resources))
	for _, r := range resources {
		got = append(got, recordedResource{
			Type:  r.Address.Type,
			ID:    r.Observed.ID,
			Name:  r.RawName,
			Attrs: r.Observed.Attributes,
		})
	}
	return got
}

func sameRecord(a, b recordedResource) bool {
	if a.Type != b.Type || a.ID != b.ID || a.Name != b.Name {
		return false
	}
	if len(a.Attrs) == 0 && len(b.Attrs) == 0 {
		return true
	}
	return maps.Equal(a.Attrs, b.Attrs)
}

// assertRecorded checks that resources are want, all of them and in order,
// and that each was listed by the aws provider.
func assertRecorded(t *testing.T, resources []harvest.Resource, want ...recordedResource) {
	t.Helper()
	got := recordedResources(resources)
	same := len(got) == len(want)
	for i := 0; same && i < len(got); i++ {
		same = sameRecord(got[i], want[i])
	}
	if !same {
		var b strings.Builder
		b.WriteString("recorded:\n")
		for _, r := range got {
			b.WriteString("  " + r.String() + "\n")
		}
		b.WriteString("want:\n")
		for _, r := range want {
			b.WriteString("  " + r.String() + "\n")
		}
		t.Error(b.String())
	}
	for _, r := range resources {
		if r.Provider != "aws" {
			t.Errorf("%s %s: provider %q, want aws", r.Address.Type, r.Observed.ID, r.Provider)
		}
	}
}

// assertRecordedError checks that err is the error the fake API answered
// with code.
func assertRecordedError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Errorf("want the %s error returned, got %v", code, err)
	}
}

// recordSecondPage reports whether call asks for the second page, by a token
// in its body or in any query parameter (nextToken, Marker, NextToken, ...).
func recordSecondPage(call apiCall) bool {
	if isSecondPage(call) {
		return true
	}
	for _, values := range call.Query {
		for _, v := range values {
			if v == nextPageToken {
				return true
			}
		}
	}
	return false
}

// recordPage is pageOf, recognising the second page by recordSecondPage.
func recordPage(call apiCall, prefix string) (id, token string) {
	if recordSecondPage(call) {
		return prefix + "-2", ""
	}
	return prefix + "-1", nextPageToken
}

// recordJSONField returns the string field key of a JSON request body, or ""
// when the body has none.
func recordJSONField(call apiCall, key string) string {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		return ""
	}
	s, _ := body[key].(string)
	return s
}

// recordJSONError is an error answer of the AWS JSON protocols.
func recordJSONError(code string) string {
	return `{"__type":"` + code + `","message":"recorded failure"}`
}

const recordEC2NS = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`

// recordEC2 is an answer of the EC2 query API to op.
func recordEC2(op, inner string) string {
	return `<` + op + `Response ` + recordEC2NS + `>` + inner + `</` + op + `Response>`
}

// recordEC2Error is an error answer of the EC2 query API. The code is
// always UnauthorizedOperation, the answer the listers must survive.
func recordEC2Error() string {
	return `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>recorded failure</Message></Error></Errors><RequestID>r-1</RequestID></Response>`
}

// recordQuery is an answer of an AWS query API (SNS, SES, Redshift, STS)
// to op, in the namespace ns.
func recordQuery(op, ns, result string) string {
	return `<` + op + `Response xmlns="` + ns + `"><` + op + `Result>` + result + `</` + op + `Result></` + op + `Response>`
}

// recordQueryError is an error answer of an AWS query API.
func recordQueryError(ns, code string) string {
	return `<ErrorResponse xmlns="` + ns + `"><Error><Type>Sender</Type><Code>` + code +
		`</Code><Message>recorded failure</Message></Error><RequestId>r-1</RequestId></ErrorResponse>`
}

// recordUnexpected fails the test for a call the lister should not make.
func recordUnexpected(t *testing.T, call apiCall) string {
	t.Helper()
	t.Errorf("unexpected call op=%q path=%q query=%q body=%s", call.Op, call.Path, call.Query.Encode(), call.Body)
	return recordJSONError("UnexpectedCall")
}

// recordJSONBool returns the boolean field key of a JSON request body, and
// whether the body sets it.
func recordJSONBool(call apiCall, key string) (value, set bool) {
	var body map[string]any
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		return false, false
	}
	value, set = body[key].(bool)
	return value, set
}

// listError lists with l, in ctx, and returns its error, for a test of
// how a lister fails.
func listError(ctx context.Context, l harvest.Lister) error {
	_, err := l.List(ctx, harvest.Settings{})
	return err
}
