// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import "testing"

// An S3 URL becomes the S3 URI the upload takes only where its bucket and
// key are safe in a shell command as they are: the URL is a template's.
func TestS3Object(t *testing.T) {
	for in, want := range map[string]string{
		"https://bkt.s3.eu-west-2.amazonaws.com/k/t.json":   "s3://bkt/k/t.json",
		"https://bkt.s3.amazonaws.com/t.json":               "s3://bkt/t.json",
		"https://my.s3.bucket.s3-eu-west-1.amazonaws.com/t": "s3://my.s3.bucket/t",
		"https://s3.eu-west-2.amazonaws.com/bkt/k/t.json":   "s3://bkt/k/t.json",
		"https://s3.amazonaws.com/bkt/t.json":               "s3://bkt/t.json",
		"https://s3.amazonaws.com/bkt":                      "",
		"https://example.com/t.json":                        "",
		"https://bkt.s3.amazonaws.com.evil.com/t.json":      "",
		"https://bkt.s3.amazonaws.com/t.json?x=1":           "",
		"https://bkt.s3.amazonaws.com/t%20$(id).json":       "",
		"https://bkt.s3.amazonaws.com/t;rm -rf ~":           "",
		"https://bkt.s3.amazonaws.com/a`id`":                "",
		"https://bkt.s3.amazonaws.com/a%0Aid":               "",
		"https://bkt.s3.amazonaws.com/a!id.json":            "",
		"https://BKT_1.s3.amazonaws.com/t.json":             "",
		"":                                                  "",
	} {
		if got := s3Object(in); got != want {
			t.Errorf("s3Object(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellArg(t *testing.T) {
	for in, want := range map[string]string{
		"app-1":                  "app-1",
		"file://.infraharvest/a": "file://.infraharvest/a",
		"it's":                   `'it'\''s'`,
		"a`b`\n$(c)":             "'ab$(c)'",
	} {
		if got := shellArg(in); got != want {
			t.Errorf("shellArg(%q) = %q, want %q", in, got, want)
		}
	}
}
