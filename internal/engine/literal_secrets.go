// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// Terraform writes into the configuration it generates every value the
// provider doesn't mark sensitive, and providers don't mark the values of
// maps and JSON documents: a Lambda function's environment variables, an
// ECS container definition, an App Service's app settings, a Cloud Run
// container's env. The functions below find the secrets among those values,
// by the name they go by and by their shape, so that Generate moves them
// into sensitive variables (see literalSecretsToVariables) and the
// verification gate fails on any that remains (see scanLiteralSecrets).

// foundSecret is a secret value written in a string literal of the
// configuration.
type foundSecret struct {
	attribute string // where it is, for people: environment.variables.API_SECRET
	key       string // the name it goes by: a map key, the name of a name/value pair, or an attribute
	value     string
	reason    string
	literal   *hclsyntax.TemplateExpr // the string it is in
	whole     bool                    // it is the whole string; else a value of the JSON document the string holds
	// outer and outerKey are the path and key of the string, for a value
	// inside its JSON document.
	outer, outerKey string
}

// tagAttribute reports whether an attribute or block holds tags or labels:
// metadata anyone who can list the resource reads, which only the
// credential formats of secretPatterns are looked for in.
func tagAttribute(name string) bool {
	switch name {
	case "tags", "tags_all", "labels", "tag", "terraform_labels", "effective_labels", "default_labels", "user_labels", "resource_labels":
		return true
	}
	return strings.HasSuffix(name, "_tags") || strings.HasSuffix(name, "_labels")
}

// secretsInBody returns the secrets in the string literals of body and its
// nested blocks, in source order. prefix is the path of body, such as
// "environment." for an environment block.
func secretsInBody(body *hclsyntax.Body, prefix string) []foundSecret {
	var found []foundSecret
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	// A name/value pair, such as a Cloud Run container's env block or a
	// CodeBuild environment_variable, names its value.
	pairName := stringLiteral(attributeExpr(body, "name"))
	if pairName == "" {
		pairName = stringLiteral(attributeExpr(body, "key"))
	}
	reference := isReferenceType(stringLiteral(attributeExpr(body, "type")))
	for _, name := range names {
		expr := body.Attributes[name].Expr
		switch {
		case tagAttribute(name):
			found = append(found, secretsInExpr(expr, prefix+name, name, false, true)...)
		case name == "value" && pairName != "" && !reference:
			found = append(found, secretsInExpr(expr, prefix+name, pairName, true, false)...)
		default:
			found = append(found, secretsInExpr(expr, prefix+name, name, false, false)...)
		}
	}
	count := map[string]int{}
	for _, b := range body.Blocks {
		count[b.Type]++
	}
	index := map[string]int{}
	for _, b := range body.Blocks {
		segment := b.Type
		if count[b.Type] > 1 {
			segment += "[" + strconv.Itoa(index[b.Type]) + "]"
		}
		index[b.Type]++
		if tagAttribute(b.Type) {
			// Tags written as blocks, such as an Auto Scaling group's.
			for _, f := range secretsInBody(b.Body, prefix+segment+".") {
				if f.reason != "" && credentialFormat(f.value) != "" {
					found = append(found, f)
				}
			}
			continue
		}
		found = append(found, secretsInBody(b.Body, prefix+segment+".")...)
	}
	return found
}

// isReferenceType reports whether the type of a name/value pair says its
// value names a secret kept elsewhere, as a CodeBuild environment
// variable of type PARAMETER_STORE or SECRETS_MANAGER does.
func isReferenceType(t string) bool {
	switch strings.ToUpper(t) {
	case "PARAMETER_STORE", "SECRETS_MANAGER", "SECRET_MANAGER", "SECRETSMANAGER":
		return true
	}
	return false
}

func attributeExpr(body *hclsyntax.Body, name string) hclsyntax.Expression {
	if attr, ok := body.Attributes[name]; ok {
		return attr.Expr
	}
	return nil
}

// stringLiteral returns the value of a string literal, "" for anything
// else (see literalString).
func stringLiteral(expr hclsyntax.Expression) string {
	t, ok := expr.(*hclsyntax.TemplateExpr)
	if !ok {
		return ""
	}
	s, _ := literalString(t)
	return s
}

// secretsInExpr returns the secrets in expr, found at path under key.
// freeForm says the key is a name someone chose, such as a map key, rather
// than an argument of the provider's schema: only then do the name and the
// shape of a value make it a secret, while credential formats count
// everywhere. Tags have only the credential formats looked for.
func secretsInExpr(expr hclsyntax.Expression, path, key string, freeForm, tags bool) []foundSecret {
	return secretsInPolicy(expr, path, key, freeForm, tags, false)
}

// secretsInPolicy is secretsInExpr below an IAM policy's Condition when
// condition is set (see secretsInJSONString).
func secretsInPolicy(expr hclsyntax.Expression, path, key string, freeForm, tags, condition bool) []foundSecret {
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr:
		value := stringLiteral(e)
		if value == "" {
			return nil
		}
		// A script, such as an instance's user data, is looked into
		// line by line: a secret it sets moves the whole script.
		if !tags && scriptKey(key) {
			if reason := scriptSecret(value); reason != "" {
				return []foundSecret{{attribute: path, key: key, value: value, reason: reason, literal: e, whole: true}}
			}
		}
		// A JSON document is looked into first, so that a secret in it
		// becomes a variable of its own and the rest keeps its text; the
		// whole value is judged only when nothing in it is a secret.
		if !tags {
			if found := secretsInJSONString(e, value, path, key); len(found) > 0 {
				return found
			}
		}
		keyName := key
		if condition {
			keyName = ""
		}
		if reason := secretReason(keyName, value, freeForm && !tags); reason != "" {
			return []foundSecret{{attribute: path, key: key, value: value, reason: reason, literal: e, whole: true}}
		}
		return nil
	case *hclsyntax.ObjectConsExpr:
		values := map[string]string{}
		for _, item := range e.Items {
			if k := objectKey(item.KeyExpr); k != "" {
				values[k] = stringLiteral(item.ValueExpr)
			}
		}
		pairName := firstOf(values, "name", "Name", "key", "Key")
		reference := isReferenceType(firstOf(values, "type", "Type"))
		var found []foundSecret
		for _, item := range e.Items {
			k := objectKey(item.KeyExpr)
			if k == "" {
				continue
			}
			itemKey := k
			if (k == "value" || k == "Value") && pairName != "" {
				if reference {
					continue
				}
				itemKey = pairName
			}
			found = append(found, secretsInPolicy(item.ValueExpr, path+pathSegment(k), itemKey, true, tags, condition || k == "Condition")...)
		}
		return found
	case *hclsyntax.TupleConsExpr:
		var found []foundSecret
		for i, item := range e.Exprs {
			found = append(found, secretsInPolicy(item, fmt.Sprintf("%s[%d]", path, i), key, freeForm, tags, condition)...)
		}
		return found
	case *hclsyntax.FunctionCallExpr:
		if e.Name == "jsonencode" && len(e.Args) == 1 {
			return secretsInPolicy(e.Args[0], path, key, true, tags, condition)
		}
	case *hclsyntax.ParenthesesExpr:
		return secretsInPolicy(e.Expression, path, key, freeForm, tags, condition)
	}
	return nil
}

func firstOf(values map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := values[k]; v != "" {
			return v
		}
	}
	return ""
}

// objectKey returns the key of an object constructor's item, "" if it
// isn't a constant.
func objectKey(expr hclsyntax.Expression) string {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return ""
	}
	return v.AsString()
}

// pathSegment renders a key as the next step of a path: .name, or
// ["key"] when it isn't an identifier.
func pathSegment(k string) string {
	if hclsyntax.ValidIdentifier(k) {
		return "." + k
	}
	return "[" + strconv.Quote(k) + "]"
}

// secretsInJSONString returns the secrets in the JSON document a string
// holds, such as a container definition Terraform didn't recognise as one.
func secretsInJSONString(e *hclsyntax.TemplateExpr, value, path, key string) []foundSecret {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var doc any
	if decoder.Decode(&doc) != nil {
		return nil
	}
	var found []foundSecret
	// condition is set below an IAM policy's Condition, whose keys are
	// AWS's condition keys, such as token.actions.githubusercontent.com:sub,
	// not names someone chose: they don't make a value a secret, though a
	// credential or a random-looking value there still is one.
	var walk func(v any, at, name string, condition bool)
	walk = func(v any, at, name string, condition bool) {
		switch v := v.(type) {
		case string:
			keyName := name
			if condition {
				keyName = ""
			}
			if reason := secretReason(keyName, v, true); reason != "" {
				found = append(found, foundSecret{attribute: at, key: name, value: v, reason: reason, literal: e, outer: path, outerKey: key})
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			pairName, _ := v["name"].(string)
			if pairName == "" {
				pairName, _ = v["Name"].(string)
			}
			for _, k := range keys {
				if tagAttribute(strings.ToLower(k)) {
					continue
				}
				itemKey := k
				if (k == "value" || k == "Value") && pairName != "" {
					itemKey = pairName
				}
				walk(v[k], at+pathSegment(k), itemKey, condition || k == "Condition")
			}
		case []any:
			for i, item := range v {
				walk(item, fmt.Sprintf("%s[%d]", at, i), name, condition)
			}
		}
	}
	walk(doc, "jsondecode("+path+")", key, false)
	return found
}

// scriptKey reports whether key is an argument or metadata key that holds
// a script an instance runs, such as an EC2 launch template's user_data,
// kept in base64, or a Compute Engine instance's startup script.
func scriptKey(key string) bool {
	switch strings.ToLower(key) {
	case "user_data", "user_data_base64", "custom_data", "metadata_startup_script", "startup_script",
		"user-data", "startup-script", "shutdown-script", "windows-startup-script-ps1", "windows-startup-script-cmd",
		"sysprep-specialize-script-ps1", "sysprep-specialize-script-cmd", "cloud-init", "cloud_init":
		return true
	}
	return false
}

var (
	// scriptAssignment finds what a line of a script sets: an environment
	// variable (NAME=value, export NAME=value, $env:NAME = value), a YAML
	// key (name: value), or a command-line option (--name value,
	// --name=value).
	scriptAssignment = []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[\s;&|(])(?:export\s+|set\s+|\$env:)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*("[^"]*"|'[^']*'|[^\s;&|]+)`),
		regexp.MustCompile(`^\s*-?\s*([A-Za-z_][A-Za-z0-9_-]*)\s*:\s+("[^"]*"|'[^']*'|\S+)\s*$`),
		regexp.MustCompile(`(?:^|\s)--([A-Za-z][A-Za-z0-9-]*)(?:=|\s+)("[^"]*"|'[^']*'|[^\s;&|-][^\s;&|]*)`),
	}
	// mysqlPassword finds the password a mysql command line gives as
	// -p<password>.
	mysqlPassword = regexp.MustCompile(`\bmysql(?:dump|admin)?\b.*\s-p("[^"]+"|'[^']+'|[^\s'"]+)`)
	// base64Text is what a base64 script, such as a launch template's user
	// data, looks like.
	base64Text = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

// scriptSecret says why the script value holds a secret, or returns "": a
// credential format anywhere in it, or a line that sets a key whose name
// says it is a secret (see secretReason) to a value of its own, not one it
// reads from elsewhere, as "$DB_PASSWORD" or "$(aws secretsmanager ...)"
// does. A script in base64 is decoded first.
func scriptSecret(value string) string {
	text := value
	if v := strings.TrimSpace(value); len(v)%4 == 0 && base64Text.MatchString(v) {
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil && utf8.Valid(decoded) {
			text = string(decoded)
		}
	}
	if name := credentialFormat(text); name != "" {
		return "the script holds what looks like a " + name
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := mysqlPassword.FindStringSubmatch(line); m != nil && scriptValue(m[1]) {
			return "the script gives mysql a password"
		}
		for _, re := range scriptAssignment {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				key, v := m[1], strings.Trim(m[2], `"'`)
				if secretKeyName(key) && scriptValue(v) && secretReason(key, v, true) != "" {
					return "the script sets " + key + ", whose name says it is a secret"
				}
			}
		}
	}
	return ""
}

// scriptValue reports whether a value a script sets is written in it, not
// read from a variable, a command or a file.
func scriptValue(v string) bool {
	v = strings.Trim(v, `"'`)
	return v != "" && !strings.ContainsAny(v[:1], "$`<@%") && !strings.Contains(v, "$(") && !strings.Contains(v, "${")
}

// secretReason says why value, going by key, is a secret, or returns "".
// Credential formats (see credentialFormat) are secrets anywhere. A value
// whose key names a secret (see secretKeyName), or that looks random
// enough to be one (see highEntropy), is a secret only where freeForm says
// the key is a name someone chose. A value whose key names a secret is one
// unless it holds no secret at all (see noSecret): spaces and digits don't
// make it plain, as a passphrase or a PIN has them. Any other value must
// also not be plainly something else (see plainValue).
func secretReason(key, value string, freeForm bool) string {
	if name := credentialFormat(value); name != "" {
		return "looks like a " + name
	}
	if !freeForm {
		return ""
	}
	if secretKeyName(key) {
		if noSecret(value) {
			return ""
		}
		return "its name says it is a secret"
	}
	if plainValue(value) {
		return ""
	}
	if !hashLikeKey(key) && highEntropy(value) {
		return "looks like a random key or token"
	}
	return ""
}

// literalSecretPatterns are credential formats looked for in single
// values, besides secretPatterns. A webhook URL is the whole value.
var literalSecretPatterns = map[string]*regexp.Regexp{
	"password in a connection string": regexp.MustCompile(`(?im)(^|;)\s*(password|pwd)\s*=\s*[^;\s]+`),
	"Azure shared access key":         regexp.MustCompile(`SharedAccessKey=[A-Za-z0-9+/=]{20,}`),
	"Azure SAS token":                 regexp.MustCompile(`[?&]sig=[A-Za-z0-9%+/=]{20,}`),
	"Slack webhook":                   regexp.MustCompile(`^https://hooks\.slack\.com/services/[A-Za-z0-9/]{20,}`),
	"Discord webhook":                 regexp.MustCompile(`^https://(ptb\.|canary\.)?discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]{20,}`),
	"Microsoft Teams webhook":         regexp.MustCompile(`^https://([A-Za-z0-9-]+\.webhook|outlook)\.office(365)?\.com/webhook(b2)?/\S{20,}`),
	"Zapier webhook":                  regexp.MustCompile(`^https://hooks\.zapier\.com/hooks/catch/[0-9]+/[A-Za-z0-9]+`),
	"GitHub fine-grained token":       regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	"GitLab token":                    regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
	"npm token":                       regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
	"API key":                         regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}\b`),
	"SendGrid key":                    regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`),
	"Stripe restricted key":           regexp.MustCompile(`\brk_live_[0-9A-Za-z]{20,}\b`),
}

var literalSecretPatternNames = sortedKeys(literalSecretPatterns)

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

// credentialFormat returns the name of the credential format value has,
// such as a private key, or "".
func credentialFormat(value string) string {
	if authorizationHeader(value) {
		return "credential in an Authorization header"
	}
	for _, name := range secretPatternNames {
		if secretPatterns[name].MatchString(value) {
			return name
		}
	}
	for _, name := range literalSecretPatternNames {
		if literalSecretPatterns[name].MatchString(value) {
			return name
		}
	}
	return ""
}

var (
	urlPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S+$`)
	pathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._@:-]+){2,}/?$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexPattern  = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9+/=_-]+$`)
)

// plainWords are values that are settings, not secrets, whatever they go
// by.
var plainWords = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true, "enabled": true, "disabled": true,
	"none": true, "null": true, "nil": true, "undefined": true, "default": true, "required": true, "optional": true,
}

// plainValue reports whether value is plainly not a secret: one that holds
// none (see noSecret), a number, or text with spaces.
func plainValue(value string) bool {
	v := strings.TrimSpace(value)
	if noSecret(v) || strings.ContainsAny(v, " \t\r\n") {
		return true
	}
	_, err := strconv.ParseFloat(v, 64)
	return err == nil
}

// noSecret reports whether value holds no secret, whatever it goes by: too
// short to be one, a setting, an ARN, a reference to a secret kept
// elsewhere (Key Vault, Secrets Manager, Secret Manager), a path, or a URL
// without credentials.
func noSecret(value string) bool {
	v := strings.TrimSpace(value)
	switch {
	case len(v) < minSecretLength, plainWords[strings.ToLower(v)]:
		return true
	case strings.HasPrefix(v, "arn:"), strings.HasPrefix(v, "@Microsoft.KeyVault("), strings.HasPrefix(v, "{{resolve:"):
		return true
	case strings.HasPrefix(v, "projects/") && strings.Contains(v, "/secrets/"):
		return true
	case pathPattern.MatchString(v) && !strings.ContainsAny(v, "+="):
		return true
	}
	if urlPattern.MatchString(v) {
		u, err := url.Parse(v)
		if err != nil {
			return false
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return false
		}
		for k := range u.Query() {
			if secretKeyName(k) || strings.EqualFold(k, "sig") || strings.EqualFold(k, "key") || strings.EqualFold(k, "code") {
				return false
			}
		}
		return true
	}
	return false
}

// keyWords splits a key into upper-case words at separators and case
// changes: dbPassword and DB_PASSWORD both give DB PASSWORD, APIKey gives
// API KEY.
func keyWords(key string) []string {
	var words []string
	var word []rune
	runes := []rune(key)
	flush := func() {
		if len(word) > 0 {
			words = append(words, strings.ToUpper(string(word)))
			word = word[:0]
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		word = append(word, r)
	}
	flush()
	return words
}

// secretKeyParts are what a key that names a secret contains, once its
// words are joined: DB_PASSWORD, clientSecret, apikey, ConnectionString.
var secretKeyParts = []string{
	"SECRET", "TOKEN", "PASSWORD", "PASSWD", "PASSPHRASE", "CREDENTIAL", "APIKEY", "PRIVATEKEY", "ACCESSKEY",
	"SIGNINGKEY", "ENCRYPTIONKEY", "MASTERKEY", "ACCOUNTKEY", "SHAREDKEY", "AUTHKEY", "HMACKEY", "LICENSEKEY",
	"CONNECTIONSTRING", "CONNSTR", "AUTHORIZATION", "BEARER", "COOKIE",
}

// secretKeyWords are words that name a secret on their own: DB_PASS,
// MYSQL_PWD.
var secretKeyWords = map[string]bool{"PWD": true, "PASS": true, "SAS": true, "PSK": true}

// identifierWords end the keys of values that identify or configure a
// secret rather than hold it: SECRET_ARN, TOKEN_URL, ACCESS_KEY_ID,
// PASSWORD_LENGTH.
var identifierWords = map[string]bool{
	"ID": true, "IDS": true, "ARN": true, "ARNS": true, "NAME": true, "NAMES": true, "URL": true, "URLS": true, "URI": true, "URIS": true,
	"PATH": true, "FILE": true, "ENDPOINT": true, "HOST": true, "HOSTNAME": true, "PORT": true, "TYPE": true, "KIND": true,
	"ENABLED": true, "DISABLED": true, "VERSION": true, "TTL": true, "LENGTH": true, "EXPIRY": true, "EXPIRATION": true,
	"EXPIRES": true, "TIMEOUT": true, "REGION": true, "ALGORITHM": true, "HEADER": true, "PARAM": true, "PARAMETER": true,
	"REF": true, "LOCATION": true, "DOMAIN": true, "DOMAINS": true, "FORMAT": true, "MODE": true, "USER": true,
	"USERNAME": true, "ISSUER": true, "AUDIENCE": true, "SCOPE": true, "SCOPES": true, "UNITS": true, "VALIDITY": true,
	"LIFETIME": true, "DURATION": true, "COUNT": true, "SIZE": true, "LIMIT": true, "POLICY": true, "ROTATION": true,
	"PROVIDER": true, "STORE": true, "VAULT": true, "SECONDS": true, "MINUTES": true, "HOURS": true, "DAYS": true,
	"INTERVAL": true, "AGE": true, "RETRIES": true, "BEHAVIOR": true, "BEHAVIOUR": true,
}

// secretKeyName reports whether key names a secret, such as API_SECRET,
// DB_PASSWORD or clientSecret, but not SECRET_ARN or ACCESS_KEY_ID.
func secretKeyName(key string) bool {
	words := keyWords(key)
	if len(words) == 0 || identifierWords[words[len(words)-1]] {
		return false
	}
	joined := strings.Join(words, "")
	for _, part := range secretKeyParts {
		if strings.Contains(joined, part) {
			return true
		}
	}
	for _, w := range words {
		if secretKeyWords[w] {
			return true
		}
	}
	return false
}

// hashWords are in the keys of values that look random without being
// secret: digests, versions, commits, image tags, IDs.
var hashWords = map[string]bool{
	"HASH": true, "SHA": true, "SHA1": true, "SHA256": true, "SHA512": true, "MD5": true, "DIGEST": true, "CHECKSUM": true,
	"VERSION": true, "COMMIT": true, "REVISION": true, "REV": true, "ETAG": true, "FINGERPRINT": true, "BUILD": true,
	"IMAGE": true, "TAG": true, "UUID": true, "GUID": true, "ID": true, "IDS": true, "ARN": true, "PUBLIC": true,
	"ACCOUNT": true, "TENANT": true, "SUBSCRIPTION": true, "PROJECT": true, "ZONE": true,
}

func hashLikeKey(key string) bool {
	words := keyWords(key)
	if len(words) > 0 && identifierWords[words[len(words)-1]] {
		return true
	}
	for _, w := range words {
		if hashWords[w] {
			return true
		}
	}
	return false
}

// highEntropy reports whether value looks like a random key or token: 20
// or more characters of hex, or of base64 mixing upper and lower case and
// digits, random enough by Shannon entropy. UUIDs and paths aren't.
func highEntropy(value string) bool {
	if len(value) < 20 || !keyPattern.MatchString(value) || uuidPattern.MatchString(value) || strings.Count(value, "/") > 2 {
		return false
	}
	if hexPattern.MatchString(value) {
		return strings.ContainsAny(value, "0123456789") && strings.ContainsAny(value, "abcdefABCDEF") && entropy(value) >= 3.0
	}
	return strings.ContainsAny(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(value, "abcdefghijklmnopqrstuvwxyz") &&
		strings.ContainsAny(value, "0123456789") && entropy(value) >= 3.5
}

// entropy is the Shannon entropy of s, in bits per byte.
func entropy(s string) float64 {
	counts := map[byte]int{}
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	h := 0.0
	for _, n := range counts {
		p := float64(n) / float64(len(s))
		h -= p * math.Log2(p)
	}
	return h
}

// awsAccessKeyIDTag finds a tag key that is an AWS access key ID.
var awsAccessKeyIDTag = regexp.MustCompile(`^(AKIA|ASIA)[0-9A-Z]{16}$`)

// accessKeyTagWarnings returns a warning for each tag of the resources in
// body whose key is an AWS access key ID, as people tag IAM users with the
// keys they hold. An ID alone isn't a secret, but it is worth a look.
func accessKeyTagWarnings(address string, body *hclsyntax.Body) []string {
	var warnings []string
	for _, name := range []string{"tags", "tags_all"} {
		obj, ok := attributeExpr(body, name).(*hclsyntax.ObjectConsExpr)
		if !ok {
			continue
		}
		for _, item := range obj.Items {
			if k := objectKey(item.KeyExpr); awsAccessKeyIDTag.MatchString(k) {
				warnings = append(warnings, fmt.Sprintf("%s has a tag named after the AWS access key ID %s…%s. An access key ID alone isn't a secret, but check that the tag is meant to be there and that the key's secret isn't kept anywhere near it", address, k[:4], k[len(k)-4:]))
			}
		}
	}
	return warnings
}

// literalSecretsToVariables moves each secret value written in the
// resources of the file at path into a new sensitive variable, named after
// the resource and the key the value goes by, other than the variables in
// taken, and rewrites the file. A value inside the JSON document of a
// string is replaced in the string (as ${jsonencode(var.name)}) where
// jsonencode writes it as the string has it; otherwise the whole string
// moves into the variable.
// It returns the variables, with the values, which infraharvest's own plans
// need (see secretValues), and warnings about tags named after AWS access
// key IDs.
func literalSecretsToVariables(path string, taken Names) ([]Secret, []string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	used := map[string]bool{}
	for name := range taken {
		if v, ok := strings.CutPrefix(name, "var."); ok {
			used[v] = true
		}
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	var secrets []Secret
	var warnings []string
	name := func(address, key string) string {
		base := strings.Trim(notInVariableName.ReplaceAllString(address+"_"+strings.ToLower(key), "_"), "_")
		n := base
		for i := 2; used[n]; i++ {
			n = fmt.Sprintf("%s_%d", base, i)
		}
		used[n] = true
		return n
	}
	for _, b := range file.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			continue
		}
		address := b.Labels[0] + "." + b.Labels[1]
		warnings = append(warnings, accessKeyTagWarnings(address, b.Body)...)
		found := secretsInBody(b.Body, "")
		// Group what is inside JSON strings by string: each string is
		// either edited in place or moved whole.
		var order []*hclsyntax.TemplateExpr
		inJSON := map[*hclsyntax.TemplateExpr][]foundSecret{}
		for _, f := range found {
			if f.whole {
				v := name(address, f.key)
				edits = append(edits, edit{f.literal.SrcRange.Start.Byte, f.literal.SrcRange.End.Byte, keepNewline(src, f.literal.SrcRange, "var."+v)})
				secrets = append(secrets, Secret{Variable: v, Address: address, Attribute: f.attribute, key: f.key, value: f.value, ty: cty.String})
				continue
			}
			if _, ok := inJSON[f.literal]; !ok {
				order = append(order, f.literal)
			}
			inJSON[f.literal] = append(inJSON[f.literal], f)
		}
		for _, literal := range order {
			start, end := literal.SrcRange.Start.Byte, literal.SrcRange.End.Byte
			text := string(src[start:end])
			inPlace := strings.HasPrefix(text, `"`)
			for _, f := range inJSON[literal] {
				if !inPlace {
					break
				}
				// The value must be in the text as it is, and jsonencode
				// must write it as the text has it, so that the plan
				// stays the same: jsonencode escapes <, >, & and the
				// Unicode line separators.
				inPlace = !strings.ContainsAny(f.value, "\"\\$%<>&\u2028\u2029") && !strings.ContainsFunc(f.value, unicode.IsControl) &&
					strings.Contains(text, `\"`+f.value+`\"`)
			}
			if !inPlace {
				whole := inJSON[literal][0]
				v := name(address, whole.outerKey)
				edits = append(edits, edit{start, end, keepNewline(src, literal.SrcRange, "var."+v)})
				secrets = append(secrets, Secret{Variable: v, Address: address, Attribute: whole.outer, key: whole.outerKey, value: stringLiteral(literal), ty: cty.String})
				continue
			}
			// jsonencode writes the quotes and escapes what the variable
			// is set to, which may need it where the value found doesn't.
			// A value the string repeats is one variable: replacing it
			// replaces every occurrence.
			byValue := map[string]bool{}
			for _, f := range inJSON[literal] {
				if byValue[f.value] {
					continue
				}
				byValue[f.value] = true
				v := name(address, f.key)
				text = strings.ReplaceAll(text, `\"`+f.value+`\"`, `${jsonencode(var.`+v+`)}`)
				secrets = append(secrets, Secret{Variable: v, Address: address, Attribute: f.attribute, key: f.key, value: f.value, ty: cty.String})
			}
			edits = append(edits, edit{start, end, text})
		}
	}
	if len(edits) == 0 {
		return nil, warnings, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	if _, diags := hclwrite.ParseConfig(out, path, hcl.InitialPos); diags.HasErrors() {
		return nil, nil, fmt.Errorf("move secrets out of %s: %w", path, diags)
	}
	return secrets, warnings, fsutil.WriteFile(path, hclwrite.Format(out), 0o644)
}

// keepNewline returns text, followed by a newline if the source range ends
// with one, as a heredoc's does.
func keepNewline(src []byte, rng hcl.Range, text string) string {
	if rng.End.Byte > 0 && rng.End.Byte <= len(src) && src[rng.End.Byte-1] == '\n' {
		return text + "\n"
	}
	return text
}

// secretValues holds the values of the secret variables infraharvest
// found in the configuration, for its own plans: in a .tfvars.json file in
// a private temporary directory outside the output, which remove deletes.
// They are never written into the output.
type secretValues struct {
	dir  string
	path string // "" while no secret has a value
}

func newSecretValues() (*secretValues, error) {
	// What a run killed before it could remove its own goes, once per
	// process.
	removeStaleOnce.Do(func() { removeStaleSecretValues(os.TempDir(), time.Now().Add(-staleSecretsAge)) })
	dir, err := os.MkdirTemp("", secretValuesPattern)
	if err != nil {
		return nil, err
	}
	return &secretValues{dir: dir}, nil
}

// The temporary directories of secretValues, and the file in each.
const (
	secretValuesPattern = "infraharvest-secrets-"
	secretValuesFile    = "secrets.tfvars.json"
)

// staleSecretsAge is how old a secretValues directory is when no run uses
// it any more: a run that was killed left it.
const staleSecretsAge = 24 * time.Hour

// removeStaleOnce has newSecretValues remove stale directories once per
// process.
var removeStaleOnce sync.Once

// removeStaleSecretValues removes the secretValues directories in parent
// that neither changed nor had their values file change since before.
func removeStaleSecretValues(parent string, before time.Time) {
	dirs, err := filepath.Glob(filepath.Join(parent, secretValuesPattern+"*"))
	if err != nil {
		return
	}
	for _, dir := range dirs {
		stale := true
		for _, path := range []string{dir, filepath.Join(dir, secretValuesFile)} {
			info, err := os.Stat(path)
			if err == nil && info.ModTime().After(before) {
				stale = false
			}
		}
		if info, err := os.Stat(dir); stale && err == nil && info.IsDir() {
			_ = os.RemoveAll(dir)
		}
	}
}

// write replaces the file with the values of the secrets that have one.
func (s *secretValues) write(secrets []Secret) error {
	values := map[string]string{}
	for _, secret := range secrets {
		if secret.value != "" {
			values[secret.Variable] = secret.value
		}
	}
	if len(values) == 0 {
		s.path = ""
		return nil
	}
	content, err := json.Marshal(values)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, secretValuesFile)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	s.path = path
	return nil
}

// options are the plan options that set the variables, if any.
func (s *secretValues) options() []tfexec.PlanOption {
	if s == nil || s.path == "" {
		return nil
	}
	return []tfexec.PlanOption{tfexec.VarFile(s.path)}
}

// remove deletes the file and its directory.
func (s *secretValues) remove() {
	if s != nil {
		_ = os.RemoveAll(s.dir)
	}
}

// redact replaces the secret values in s, such as a diagnostic that quotes
// the value it rejects, with "(sensitive value)", in the escaped forms text
// quotes them in too (see textForms).
func redact(s string, secrets []Secret) string {
	return redactText(s, textForms(knownValues(secrets)))
}

// redactBytes is redact for file content.
func redactBytes(b []byte, secrets []Secret) []byte {
	return []byte(redact(string(b), secrets))
}

// scanLiteralSecrets returns, for a .tf or .hcl file, a finding for each
// secret value secretsInBody finds in any of its blocks.
func scanLiteralSecrets(rel, content string) []string {
	if !strings.HasSuffix(rel, ".tf") && !strings.HasSuffix(rel, ".hcl") {
		return nil
	}
	body, ok := parseBody(rel, content)
	if !ok {
		return nil
	}
	var findings []string
	for _, b := range body.Blocks {
		what := strings.Join(append([]string{b.Type}, b.Labels...), ".")
		for _, f := range secretsInBody(b.Body, "") {
			findings = append(findings, fmt.Sprintf("%s: %s of %s holds a secret value (%s); it belongs in a sensitive variable", rel, f.attribute, what, f.reason))
		}
	}
	return findings
}

// withoutRejected returns the secrets of resources rejections doesn't
// name.
func withoutRejected(secrets []Secret, rejections []Rejection) []Secret {
	left := map[string]bool{}
	for _, r := range rejections {
		left[r.Address] = true
	}
	var kept []Secret
	for _, s := range secrets {
		if !left[s.Address] {
			kept = append(kept, s)
		}
	}
	return kept
}

// withVariables returns taken with the variables of secrets added.
func withVariables(taken Names, secrets []Secret) Names {
	if len(secrets) == 0 {
		return taken
	}
	names := Names{}
	for name := range taken {
		names[name] = true
	}
	for _, s := range secrets {
		names["var."+s.Variable] = true
	}
	return names
}

// knownValues returns the values of the secrets infraharvest found, which
// no file may contain.
func knownValues(secrets []Secret) []string {
	var values []string
	for _, s := range secrets {
		if len(s.value) >= minSecretLength {
			values = append(values, s.value)
		}
	}
	return values
}
