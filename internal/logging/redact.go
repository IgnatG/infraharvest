// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import "regexp"

// redacted is what stands in for a credential RedactHTTP removes.
const redacted = "[redacted]"

// secretHeader matches a header line of an HTTP request or response dump
// whose value is a credential: the signature and session token a request
// is signed with, cookies, and any other header named as a token.
var secretHeader = regexp.MustCompile(`(?im)^(authorization|proxy-authorization|cookie|set-cookie|x-amz-security-token|x-amz-credential|x-amz-signature|[a-z0-9-]*-token|[a-z0-9-]*-api-key):[^\r\n]*`)

// secretQuery matches the query parameters of a presigned request that
// carry its credential, signature or session token.
var secretQuery = regexp.MustCompile(`(?i)\b(x-amz-credential|x-amz-signature|x-amz-security-token)=[^&\s]*`)

// RedactHTTP returns dump, an HTTP request or response as a client
// library logs it, with the values of the headers and query parameters
// that hold credentials replaced. Everything else, the method, URL,
// status and other headers, is kept.
func RedactHTTP(dump string) string {
	dump = secretHeader.ReplaceAllString(dump, "${1}: "+redacted)
	return secretQuery.ReplaceAllString(dump, "${1}="+redacted)
}
