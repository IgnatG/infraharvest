// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// sensitiveValues returns the string values the plan marks sensitive in
// the imported objects: what must never be written into a file. A value
// publicSensitive says isn't secret, such as a String parameter's, is left
// out, and so is one that is also the whole of an attribute the plan
// doesn't mark sensitive, where it plainly isn't a secret (see
// structural), such as an SSM parameter (whose value the provider always
// marks sensitive) holding a region name.
func sensitiveValues(p *tfjson.Plan) []string {
	if p == nil {
		return nil
	}
	seen, public, notSecret := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil {
			collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, seen)
			collectPublic(rc.Change.Before, rc.Change.BeforeSensitive, public)
			if v, ok := publicValue(rc); ok {
				notSecret[v] = true
			}
		}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		if !notSecret[v] && (!public[v] || !structural(v)) {
			values = append(values, v)
		}
	}
	sort.Strings(values)
	return values
}

// identifierLike is what names and IDs look like: lower-case words and
// digits joined by dashes, such as eu-west-2, vpc-0abc1234 or app-prod.
var identifierLike = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)

// structural reports whether a value the plan marks sensitive somewhere
// is plainly not a secret where another attribute holds it in the open:
// it holds none (see noSecret), it is a number or text with spaces, or it
// looks like a region, a name or an ID. A value that could be a password
// or a token, with upper case, punctuation or the randomness of a key,
// stays a secret even then, as a copy of a secret in an attribute the
// provider doesn't mark is the leak G6 is for.
func structural(v string) bool {
	return plainValue(v) || identifierLike.MatchString(v)
}

// collectPublic adds the strings in value that mask doesn't mark sensitive.
func collectPublic(value, mask any, out map[string]bool) {
	if marked, ok := mask.(bool); ok && marked {
		return
	}
	switch v := value.(type) {
	case string:
		out[v] = true
	case map[string]any:
		m, _ := mask.(map[string]any)
		for k, sub := range v {
			collectPublic(sub, m[k], out)
		}
	case []any:
		m, _ := mask.([]any)
		for i, sub := range v {
			var subMask any
			if i < len(m) {
				subMask = m[i]
			}
			collectPublic(sub, subMask, out)
		}
	}
}

// collectSensitive adds the strings in value that mask marks sensitive.
func collectSensitive(value, mask any, out map[string]bool) {
	if marked, ok := mask.(bool); ok && marked {
		addStrings(value, out)
		return
	}
	switch m := mask.(type) {
	case map[string]any:
		v, _ := value.(map[string]any)
		for k, sub := range m {
			collectSensitive(v[k], sub, out)
		}
	case []any:
		v, _ := value.([]any)
		for i, sub := range m {
			if i < len(v) {
				collectSensitive(v[i], sub, out)
			}
		}
	}
}

// minSecretLength leaves out values too short to tell apart from other
// text, such as "true".
const minSecretLength = 6

func addStrings(value any, out map[string]bool) {
	switch v := value.(type) {
	case string:
		if len(v) >= minSecretLength {
			out[v] = true
		}
	case map[string]any:
		for _, sub := range v {
			addStrings(sub, out)
		}
	case []any:
		for _, sub := range v {
			addStrings(sub, out)
		}
	}
}

// credentialPatterns are formats of credentials that never belong in
// configuration, sorted by name, so that findings come in the same order
// every run. scanFile looks for them in every file the gate reads, a
// README as much as a .tf file, and credentialFormat in every single value.
// An AWS access key ID on its own isn't a credential (see awsKeyPair).
var credentialPatterns = []credentialPattern{
	credential("API key", `\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}\b`),
	credential("Azure SAS token", `[?&]sig=[A-Za-z0-9%+/=]{20,}`),
	credential("Azure shared access key", `SharedAccessKey=[A-Za-z0-9+/=]{20,}`),
	credential("Azure storage key", `AccountKey=[A-Za-z0-9+/=]{40,}`),
	wholeValueCredential("Discord webhook", `https://(ptb\.|canary\.)?discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]{20,}`),
	credential("GitHub fine-grained token", `\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	credential("GitHub token", `\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	credential("GitLab token", `\bglpat-[A-Za-z0-9_-]{20,}\b`),
	credential("Google API key", `\bAIza[0-9A-Za-z_-]{35}\b`),
	credential("JSON Web Token", `\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	wholeValueCredential("Microsoft Teams webhook", `https://([A-Za-z0-9-]+\.webhook|outlook)\.office(365)?\.com/webhook(b2)?/\S{20,}`),
	credential("SendGrid key", `\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`),
	credential("Slack token", `\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	wholeValueCredential("Slack webhook", `https://hooks\.slack\.com/services/[A-Za-z0-9/]{20,}`),
	credential("Stripe restricted key", `\brk_live_[0-9A-Za-z]{20,}\b`),
	credential("Stripe secret key", `\bsk_live_[0-9A-Za-z]{20,}\b`),
	wholeValueCredential("Zapier webhook", `https://hooks\.zapier\.com/hooks/catch/[0-9]+/[A-Za-z0-9]+`),
	credential("npm token", `\bnpm_[A-Za-z0-9]{36}\b`),
	credential("password in a URL", `[a-z][a-z0-9+.-]*://[^/\s:@"]+:[^/\s@"]+@`),
	valueCredential("password in a connection string", `(?im)(^|;)\s*(password|pwd)\s*=\s*[^;\s]+`),
	credential("private key", `-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// credentialPattern is a format of credential in credentialPatterns.
type credentialPattern struct {
	name string
	// inFile matches the credential anywhere in a file; nil for a format
	// that only a single value tells (see valueCredential).
	inFile *regexp.Regexp
	// inValue matches it in a single value.
	inValue *regexp.Regexp
}

// credential is a format found the same way anywhere in a file or a value.
func credential(name, pattern string) credentialPattern {
	re := regexp.MustCompile(pattern)
	return credentialPattern{name: name, inFile: re, inValue: re}
}

// wholeValueCredential is a format, such as a webhook URL, that is the
// whole of a value that holds it, so a value must start with it; a file
// can hold it anywhere.
func wholeValueCredential(name, pattern string) credentialPattern {
	return credentialPattern{name: name, inFile: regexp.MustCompile(pattern), inValue: regexp.MustCompile("^" + pattern)}
}

// valueCredential is a format that only a single value tells: a password
// in a connection string, which in a file would match every password
// argument, such as one set from a variable.
func valueCredential(name, pattern string) credentialPattern {
	return credentialPattern{name: name, inValue: regexp.MustCompile(pattern)}
}

// scanSecrets checks every file Generate wrote into dir, and into the
// local modules it calls (see walkOutputAndModules), for the plan's
// sensitive values and the secret values Generate found, for credential
// formats, for provider blocks that set a credential argument to a
// constant (see isCredential), and for any secret value written in
// the configuration (see literalSecrets).
func scanSecrets(dir string, sensitive []string) (Check, error) {
	check := Check{Name: CheckSecrets, Passed: true}
	values := newValueMatcher(sensitive)
	err := walkOutputAndModules(dir, func(rel string, content string) {
		scanFile(&check, values, rel, content)
	})
	return check, err
}

// valueMatcher finds whether a text holds any of a set of values, in one
// pass over the text however many values there are: each run of
// minSecretLength bytes of the text is looked up among the values' first
// bytes, and only the values that start so are compared. A root may have
// thousands of sensitive values, and a strings.Contains for each would read
// every file that many times.
type valueMatcher struct {
	byPrefix map[uint64][]string
	// short are the values shorter than minSecretLength, compared one by
	// one.
	short []string
}

func newValueMatcher(values []string) valueMatcher {
	m := valueMatcher{byPrefix: map[uint64][]string{}}
	for _, v := range values {
		if len(v) < minSecretLength {
			m.short = append(m.short, v)
			continue
		}
		key := prefixKey(v)
		m.byPrefix[key] = append(m.byPrefix[key], v)
	}
	return m
}

// prefixKey packs the first minSecretLength bytes of s into a number.
func prefixKey(s string) uint64 {
	var key uint64
	for i := range minSecretLength {
		key = key<<8 | uint64(s[i])
	}
	return key
}

// in reports whether content holds one of the values.
func (m valueMatcher) in(content string) bool {
	for _, v := range m.short {
		if strings.Contains(content, v) {
			return true
		}
	}
	if len(m.byPrefix) == 0 {
		return false
	}
	for i := 0; i+minSecretLength <= len(content); i++ {
		for _, v := range m.byPrefix[prefixKey(content[i:])] {
			if strings.HasPrefix(content[i:], v) {
				return true
			}
		}
	}
	return false
}

// scanFile runs scanSecrets' checks on one file, rel, adding what it finds
// to check.
func scanFile(check *Check, sensitive valueMatcher, rel, content string) {
	if sensitive.in(content) {
		check.Passed = false
		check.Details = append(check.Details, rel+": contains a value the provider marks sensitive")
	}
	for _, p := range credentialPatterns {
		if p.inFile != nil && p.inFile.MatchString(content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": looks like it contains a "+p.name)
		}
	}
	if awsKeyPair(content) {
		check.Passed = false
		check.Details = append(check.Details, rel+": looks like it contains an AWS access key ID with its secret access key")
	}
	// Parsed once for the checks below, and only a file they read:
	// parsing is most of what a scan costs. A file that doesn't parse
	// has nothing to scan, since validate (G2) reports it.
	if !readsLiteralSecrets(rel) {
		return
	}
	parsed, err := parseHCL(rel, []byte(content))
	if err != nil {
		return
	}
	for _, name := range providerCredentials(parsed) {
		check.Passed = false
		check.Details = append(check.Details, rel+": provider block sets "+name+", which belongs in the provider's environment")
	}
	// What Generate moves into variables, should any remain or come
	// back with an edit.
	if findings := literalSecrets(parsed); len(findings) > 0 {
		check.Passed = false
		check.Details = append(check.Details, findings...)
	}
}

// checkReadme runs scanSecrets' checks on the README Generate wrote after
// the gate ran, which the gate couldn't see, with values the secret values
// no file may contain. Should the README hold one, G6 fails and the README
// is written again to say so.
func checkReadme(dir string, result *Result, values []string) error {
	content, err := os.ReadFile(filepath.Join(dir, ReadmeFileName))
	if err != nil {
		return err
	}
	found := Check{Passed: true}
	scanFile(&found, newValueMatcher(values), ReadmeFileName, string(content))
	if found.Passed {
		return nil
	}
	for i, c := range result.Gate {
		if c.Name == CheckSecrets {
			result.Gate[i].Passed = false
			result.Gate[i].Details = append(result.Gate[i].Details, found.Details...)
		}
	}
	return WriteReadme(dir, result)
}

// providerCredentials returns the credential arguments the provider blocks
// of a .tf file set to constants, sorted, as "provider.argument".
func providerCredentials(f *hclFile) []string {
	if !strings.HasSuffix(f.path, ".tf") {
		return nil
	}
	var names []string
	for _, b := range f.syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 {
			continue
		}
		for name, attr := range b.Body.Attributes {
			if !isCredential(name) {
				continue
			}
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.IsWhollyKnown() {
				names = append(names, b.Labels[0]+"."+name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// awsAccessKeyID and awsSecretCandidate find the two halves of an AWS
// access key: the ID, and 40 characters of base64 that could be its secret.
var (
	awsAccessKeyID     = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	awsSecretCandidate = regexp.MustCompile(`(^|[^A-Za-z0-9/+=])([A-Za-z0-9/+]{40})($|[^A-Za-z0-9/+=])`)
)

// awsKeyPair reports whether content holds an AWS access key ID and,
// anywhere in the same file, what looks like a secret access key: 40
// characters of base64 mixing upper and lower case letters and digits,
// which a hex digest such as a commit SHA doesn't. An ID on its own is an
// identifier, such as an IAM access key's import ID or a tag naming one,
// and can't sign a request; GitHub's secret scanning matches the pair too.
func awsKeyPair(content string) bool {
	if !awsAccessKeyID.MatchString(content) {
		return false
	}
	for _, m := range awsSecretCandidate.FindAllStringSubmatch(content, -1) {
		s := m[2]
		if strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") && strings.ContainsAny(s, "0123456789") {
			return true
		}
	}
	return false
}
