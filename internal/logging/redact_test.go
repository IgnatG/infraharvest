// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"strings"
	"testing"
)

// The credentials of a logged request and response are replaced; the rest
// of the dump is kept.
func TestRedactHTTPRemovesCredentials(t *testing.T) {
	dump := "Request\r\n" +
		"GET /bucket/key?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20260101%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=deadbeef&X-Amz-Security-Token=FQoGsession HTTP/1.1\r\n" +
		"Host: s3.us-east-1.amazonaws.com\r\n" +
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260101/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=cafef00d\r\n" +
		"X-Amz-Security-Token: FQoGsessiontoken\r\n" +
		"x-goog-api-key: AIzaSecret\r\n" +
		"Cookie: session=secret\r\n" +
		"X-Amz-Date: 20260101T000000Z\r\n" +
		"\r\n" +
		"Response\r\n" +
		"HTTP/1.1 200 OK\r\n" +
		"Set-Cookie: id=secret\r\n" +
		"X-Amzn-Requestid: 1234\r\n"

	got := RedactHTTP(dump)

	for _, secret := range []string{"AKIDEXAMPLE", "deadbeef", "cafef00d", "FQoGsession", "AIzaSecret", "session=secret", "id=secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q left in\n%s", secret, got)
		}
	}
	for _, kept := range []string{
		"GET /bucket/key?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=[redacted]&X-Amz-Signature=[redacted]&X-Amz-Security-Token=[redacted] HTTP/1.1\r\n",
		"Host: s3.us-east-1.amazonaws.com\r\n",
		"Authorization: [redacted]\r\n",
		"X-Amz-Security-Token: [redacted]\r\n",
		"X-Amz-Date: 20260101T000000Z\r\n",
		"HTTP/1.1 200 OK\r\n",
		"X-Amzn-Requestid: 1234\r\n",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing from\n%s", kept, got)
		}
	}
}
