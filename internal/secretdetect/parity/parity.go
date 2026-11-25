// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package parity holds what tests of the secret detectors share: a corpus
// of keys, values and tags, and the output the detectors gave for it
// before they moved out of internal/engine/secret and internal/export into
// internal/secretdetect and pkg/secretscan (testdata/parity.golden). Each
// package that offers the detectors checks itself against it, so that
// they all give the same answers byte for byte as the original code did.
// The golden file names a key or a value by its place in Corpus, and holds
// neither: the corpus has credentials in every format the detectors know,
// made of pieces so that no file holds one whole, and the golden file must
// not either.
// It is for tests only.
package parity

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
)

//go:embed testdata/parity.golden
var golden string

// Detectors are the functions of a package that offers the secret
// detectors. Any may be nil, and the cases of what it gives are then left
// out of the check.
type Detectors struct {
	// IsName tells whether a key names a secret.
	IsName func(key string) bool
	// CredentialFormat names the credential format a value has, or "".
	CredentialFormat func(value string) string
	// Reason says why a value, going by its key, is a secret, or "".
	Reason func(key, value string, freeForm bool) string
	// LooksSecret tells whether a tag's key or value looks like a secret.
	LooksSecret func(key, value string) bool
	// InFile returns what the secrets check finds in a file.
	InFile func(rel, content string) []string
	// NewTagger makes a tag filter that drops the tag keys drop, or all
	// tags, and hashes with key if it isn't nil.
	NewTagger func(drop []string, all bool, key []byte) Tagger
}

// Tagger is a tag filter and the hash it hashes IDs with.
type Tagger interface {
	// Tags returns the tags of a resource with the ID id and the name
	// name that stay in an export.
	Tags(tags map[string]string, id, name string) map[string]string
	// Dropped returns the tag keys dropped, sorted.
	Dropped() []string
	// Hash returns the hash of s, or s itself without a key.
	Hash(s string) string
}

// Section is a part of the golden file, which one kind of detector gives.
type Section struct {
	Name, Text string
}

// Check compares what d gives with the golden file, section by section,
// for the sections d can give.
func Check(t *testing.T, d Detectors) {
	t.Helper()
	want := map[string]string{}
	var name string
	for _, line := range strings.SplitAfter(golden, "\n") {
		if n, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "## "); ok {
			name = n
			continue
		}
		want[name] += line
	}
	sections := Sections(d)
	if len(sections) == 0 {
		t.Fatal("no detector to check")
	}
	for _, s := range sections {
		if s.Text == want[s.Name] {
			continue
		}
		gotLines, wantLines := strings.Split(s.Text, "\n"), strings.Split(want[s.Name], "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Fatalf("%s: the detectors no longer give what they gave when they moved, from line %d:\n got %s\nwant %s", s.Name, i+1, g, w)
			}
		}
	}
}

// Golden returns the sections of sections as the golden file holds them.
func Golden(sections ...[]Section) string {
	var b strings.Builder
	for _, list := range sections {
		for _, s := range list {
			fmt.Fprintf(&b, "## %s\n%s", s.Name, s.Text)
		}
	}
	return b.String()
}

// Sections returns what d gives for the corpus, as text, a section for
// each kind of detector d has.
func Sections(d Detectors) []Section {
	keys, values := Corpus()
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	var sections []Section
	if d.IsName != nil {
		var b strings.Builder
		for i, k := range keys {
			fmt.Fprintf(&b, "%d\t%v\n", i, d.IsName(k))
		}
		sections = append(sections, Section{"name", b.String()})
	}
	if d.CredentialFormat != nil {
		var b strings.Builder
		for i, s := range append(append([]string{}, keys...), values...) {
			fmt.Fprintf(&b, "%d\t%s\n", i, q(d.CredentialFormat(s)))
		}
		sections = append(sections, Section{"format", b.String()})
	}
	if d.Reason != nil {
		sections = append(sections, Section{"reason", reasons(d.Reason, keys, values)})
	}
	if d.LooksSecret != nil {
		var b strings.Builder
		for i, k := range keys {
			fmt.Fprintf(&b, "%d\t", i)
			for _, v := range values {
				b.WriteString(map[bool]string{true: "y", false: "n"}[d.LooksSecret(k, v)])
			}
			b.WriteString("\n")
		}
		sections = append(sections, Section{"looks", b.String()})
	}
	if d.InFile != nil {
		var b strings.Builder
		for i, v := range values {
			fmt.Fprintf(&b, "%d\t%s\n", i, q(strings.Join(d.InFile("f.md", v), "|")))
		}
		sections = append(sections, Section{"infile", b.String()})
	}
	if d.NewTagger != nil {
		sections = append(sections, Section{"tags", tags(d.NewTagger)})
	}
	return sections
}

// reasons writes the reason of each key and value, with and without
// freeForm. The reasons are few: each gets a code, so that the list of
// which pairs give which stays small.
func reasons(reason func(key, value string, freeForm bool) string, keys, values []string) string {
	var table []string
	code := func(r string) string {
		if r == "" {
			return "-"
		}
		for i, known := range table {
			if known == r {
				return fmt.Sprintf("%c", '0'+i)
			}
		}
		table = append(table, r)
		return fmt.Sprintf("%c", '0'+len(table)-1)
	}
	var lines strings.Builder
	for i, k := range keys {
		var free, fixed strings.Builder
		for _, v := range values {
			free.WriteString(code(reason(k, v, true)))
			fixed.WriteString(code(reason(k, v, false)))
		}
		fmt.Fprintf(&lines, "%d\t%s\t%s\n", i, free.String(), fixed.String())
	}
	var b strings.Builder
	for i, r := range table {
		fmt.Fprintf(&b, "%c\t%q\n", '0'+i, r)
	}
	return b.String() + lines.String()
}

// tags writes what a tag filter gives for tag sets under each of the
// settings --drop-tag, --drop-tags and --hash-ids make.
func tags(newTagger func(drop []string, all bool, key []byte) Tagger) string {
	var b strings.Builder
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	random := sum("tags")
	tagSets := []map[string]string{
		nil,
		{},
		{"Name": "web", "Environment": "prod", "owner": "platform"},
		{"Name": "logs-bucket", "alias": "logs", "arn": "arn:aws:s3:::logs-bucket", "url": "https://logs-bucket.s3.amazonaws.com/x"},
		{"kubernetes.io/cluster/logs-bucket": "owned", "env": "prod", "team": "a"},
		{"db_password": "hunter2x", "env": "prod", "note": "keep the API secret safe"},
		{"token": random[:24], "checksum": random, "version": "1.2.3", "commit": random[:40]},
		{"gh" + "p_" + strings.Repeat("a1", 18): "x", "k": "gh" + "p_" + strings.Repeat("a1", 18), "ok": "fine"},
		{"cost-center": "1234", "Cost Center": "CC-99", "Application": "logs", "id": "logs-bucket-1"},
		{"short": "ab", "x": "a", "n": "web-1", "Name": "a"},
		{"auth": "Bearer " + random[:24], "connection": "Server=db;Password=" + "hunter2x", "region": "eu-west-2"},
	}
	settings := []struct {
		name string
		drop []string
		all  bool
		key  []byte
	}{
		{"plain", nil, false, nil},
		{"drop-tag", []string{"owner", "env", "alias"}, false, nil},
		{"drop-tags", nil, true, nil},
		{"hash", nil, false, []byte("0123456789abcdef-key")},
		{"hash+drop", []string{"owner", "kubernetes.io/cluster/logs-bucket"}, false, []byte("0123456789abcdef-key")},
		{"hash+drop-tags", nil, true, []byte("0123456789abcdef-key")},
	}
	ids := [][2]string{{"", ""}, {"logs-bucket", "logs"}, {"web", "web"}, {"vpc-0abc1234", "main"}}
	for _, s := range settings {
		for i, tags := range tagSets {
			for _, id := range ids {
				f := newTagger(s.drop, s.all, s.key)
				got := f.Tags(tags, id[0], id[1])
				keys := make([]string, 0, len(got))
				for k := range got {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var kept []string
				for _, k := range keys {
					kept = append(kept, q(k)+"="+q(got[k]))
				}
				fmt.Fprintf(&b, "T\t%s\t%d\t%s\t%s\t%s\t[%s]\t%s\n", s.name, i, q(id[0]), q(id[1]), "tags", strings.Join(kept, " "), q(strings.Join(f.Dropped(), ",")))
			}
		}
		f := newTagger(s.drop, s.all, s.key)
		for _, v := range []string{"", "a", "vpc-0abc1234", "arn:aws:s3:::logs-bucket", "module.logs.aws_s3_bucket.this[0]", "ünï"} {
			fmt.Fprintf(&b, "H\t%s\t%s\t%s\n", s.name, q(v), q(f.Hash(v)))
		}
	}
	return b.String()
}

func sum(seed string) string {
	s := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(s[:])
}

// Corpus returns the keys and values the detectors are run over: names
// and values of every kind they tell apart. The credentials are made up
// of pieces, so that no file of the repository holds one in a format a
// secret scanner knows.
func Corpus() (keys, values []string) {
	random := sum("one")
	keys = []string{
		"", "password", "DB_PASSWORD", "dbPassword", "clientSecret", "apiKey", "API_KEY", "APIKey", "SECRET_ARN",
		"ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "Name", "name", "Sid", "sid", "SESSION_SID", "token", "TOKEN_URL",
		"TOKEN_ENABLED", "commit_sha", "source_code_hash", "image_tag", "Authorization", "authorization", "cookie",
		"user_data", "PWD", "DB_PASS", "sas_token", "region", "Environment", "owner", "description",
		"kubernetes.io/cluster/prod", "aws:cloudformation:stack-name", "connection_string", "ConnectionString",
		"secret", "SECRET", "x", "headers", "WEBHOOK_URL", "TENANT", "private_key", "PASSWORD_MIN_LENGTH",
		"https://user:" + "pw123456@db.example.com/app", "Bearer " + random[:24],
		"gh" + "p_" + strings.Repeat("a1", 18),
	}
	values = []string{
		"", "a", "abc", "true", "false", "disabled", "none", "12", "86400000", "84629137", "hunter2x",
		"correct-horse", "correct horse battery staple", "abc.DEF-123_xyz", "minutes", "eu-west-2", "vpc-0abc1234",
		"app-prod", "Production", "web", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", "/prod/db/password", "/a/b/c",
		"arn:aws:secretsmanager:eu-west-2:123456789012:secret:api", "arn:aws:iam::123456789012:role/admin",
		"{{resolve:secretsmanager:prod/db:SecretString:password}}", "@Microsoft.KeyVault(SecretUri=https://v.vault.azure.net/secrets/x)",
		"projects/1/secrets/db/versions/1", "https://example.com/docs", "https://login.example.com/oauth2/token",
		"https://vault.example.com/v1/secret/api?token=abc", "https://example.com/?sig=" + random[:24],
		"postgres://app:" + "pw123456@db:5432/app", "postgres://app@db:5432/app",
		random, random[:20], random[:32], random[:40], strings.ToUpper(random[:30]),
		"Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2uV3wX4yZ", "Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2",
		"AWSLogDeliveryWrite3fG9kQ2xR7mT", "keep the API secret safe", "Basic authentication for the admin pages",
		"Bearer authentication", "Bearer " + random[:24], "bearer " + random[:16],
		"Basic " + base64.StdEncoding.EncodeToString([]byte("user:pa55word")),
		"Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")),
		"AKIA" + "EXAMPLE" + "EXAMPLE12", "ASIA" + "EXAMPLE" + "EXAMPLE12",
		"gh" + "p_" + strings.Repeat("a1", 18), "github" + "_pat_" + random[:40] + "x", "gl" + "pat-" + random[:20],
		"sk" + "-ant-" + random[:30], "sk" + "-proj-" + random[:30], "sk_li" + "ve_" + random[:24], "rk_li" + "ve_" + random[:24],
		"xo" + "xb-" + "1234567890-abcdef", "SG" + "." + random[:22] + "." + random[:43], "np" + "m_" + random[:36],
		"AI" + "za" + random[:35], "eyJ" + random[:12] + ".eyJ" + random[:12] + "." + random[:12],
		"-----BEGIN RSA PRIVATE" + " KEY-----", "-----BEGIN PRIVATE" + " KEY-----",
		"https://hooks." + "slack.com/services/" + random[:24], "https://discord" + ".com/api/webhooks/123456789012345678/" + random[:40],
		"https://contoso.webhook.office" + ".com/webhookb2/" + random[:36] + "/IncomingWebhook/" + random[:32],
		"https://hooks." + "zapier.com/hooks/catch/123456/" + random[:8],
		"https://discord.com/channels/1234", "Server=db;Password=" + "hunter2x;User=app", "Server=db;User=app",
		"AccountKey=" + strings.Repeat("Ab1+", 12), "SharedAccessKey=" + strings.Repeat("Ab1+", 8),
		"https://a.blob.core.windows.net/c?sv=1&sig=" + strings.Repeat("Ab1%2B", 6),
		"export DB_PASSWORD=" + "hunter2x", "#!/bin/sh\nexport API_TOKEN=" + random[:24] + "\n",
		"12.5", "1e9", " padded value ", "line\nbreak", "ünïcode-välue-with-ümlauts-123456789", strings.Repeat("a", 100),
	}
	return keys, values
}
