// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package secretdetect decides whether a name or a value is a secret: the
// detectors the secrets check (G6), the export's tag filter and the drift
// summary share, and pkg/secretscan offers. It is a leaf: it imports none
// of infraharvest's packages, nor Terraform or HCL, so that anything can
// use it without bringing the engine in, and its answers are one set, so
// that what an export drops is what the check finds.
package secretdetect

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// MinSecretLength is the shortest value that can be told apart from other
// text: a shorter one, such as "true", is left out of what is looked for.
const MinSecretLength = 6

// PlainValue reports whether value is plainly not a secret: one that holds
// none (see noSecret), a number, or text with spaces.
func PlainValue(value string) bool {
	return plainValue(value)
}

// credentialPatterns are formats of credentials that never belong in
// configuration, sorted by name, so that findings come in the same order
// every run. FileCredentials looks for them in every file the gate reads,
// a README as much as a .tf file, and CredentialFormat in every single
// value. An AWS access key ID on its own isn't a credential.
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

// FileCredentials returns the names of the credential formats content
// holds anywhere, such as "GitHub token", in the order of the formats
// (sorted by name).
func FileCredentials(content string) []string {
	var names []string
	for _, p := range credentialPatterns {
		if p.inFile != nil && p.inFile.MatchString(content) {
			names = append(names, p.name)
		}
	}
	return names
}

// Reason says why value, going by key, is a secret, or returns "".
// Credential formats (see CredentialFormat) are secrets anywhere. A value
// whose key names a secret (see IsName), or that looks random
// enough to be one (see highEntropy), is a secret only where freeForm says
// the key is a name someone chose. A value whose key names a secret is one
// unless it holds no secret at all (see noSecret): spaces and digits don't
// make it plain, as a passphrase or a PIN has them. Any other value must
// also not be plainly something else (see plainValue). An IAM policy
// statement's Sid is its ID, however random it looks, such as the
// AWSLogDeliveryWrite<digits> AWS gives a flow log's bucket (see policySid);
// SESSION_SID and the like are still checked.
func Reason(key, value string, freeForm bool) string {
	if name := CredentialFormat(value); name != "" {
		return "looks like a " + name
	}
	if !freeForm {
		return ""
	}
	if IsName(key) {
		if noSecret(value) {
			return ""
		}
		return "its name says it is a secret"
	}
	if plainValue(value) {
		return ""
	}
	if !hashLikeKey(key) && !policySid(key, value) && highEntropy(value) {
		return "looks like a random key or token"
	}
	return ""
}

// policySid reports whether key and value are an IAM policy statement's
// Sid: the key Sid as policies spell it, and a value IAM takes as one.
func policySid(key, value string) bool {
	return key == "Sid" && sidPattern.MatchString(value)
}

// authorizationPattern matches the value of an Authorization header with
// the Basic or Bearer scheme.
var authorizationPattern = regexp.MustCompile(`^(?i)(basic|bearer)\s+([A-Za-z0-9._~+/-]+=*)$`)

// authorizationHeader reports whether value is an Authorization header's
// credentials, as a scheduler job or an uptime check sends them: Basic with
// the base64 of user:password, or Bearer with a token of 16 or more
// characters that has digits or mixes upper and lower case, which the
// words of a description don't.
func authorizationHeader(value string) bool {
	m := authorizationPattern.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return false
	}
	token := m[2]
	if strings.EqualFold(m[1], "basic") {
		decoded, err := base64.StdEncoding.DecodeString(token)
		return err == nil && strings.Contains(string(decoded), ":")
	}
	return len(token) >= 16 && (strings.ContainsAny(token, "0123456789") ||
		(strings.ContainsAny(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(token, "abcdefghijklmnopqrstuvwxyz")))
}

// CredentialFormat returns the name of the credential format value has,
// such as a private key, or "".
func CredentialFormat(value string) string {
	if authorizationHeader(value) {
		return "credential in an Authorization header"
	}
	for _, p := range credentialPatterns {
		if p.inValue.MatchString(value) {
			return p.name
		}
	}
	return ""
}
