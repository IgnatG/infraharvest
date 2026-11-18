// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// Terraform writes into the configuration it generates every value the
// provider doesn't mark sensitive, and providers don't mark the values of
// maps and JSON documents: a Lambda function's environment variables, an
// ECS container definition, an App Service's app settings, a Cloud Run
// container's env. The functions below find the secrets among those values,
// by the name they go by and by their shape, so that Generate moves them
// into sensitive variables (see LiteralsToVariables) and the
// verification gate fails on any that remains (see Literals).

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
// credential formats of credentialPatterns are looked for in.
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
	pairName := hclfile.StringLiteral(hclfile.AttributeExpr(body, "name"))
	if pairName == "" {
		pairName = hclfile.StringLiteral(hclfile.AttributeExpr(body, "key"))
	}
	reference := IsReferenceType(hclfile.StringLiteral(hclfile.AttributeExpr(body, "type")))
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
				if f.reason != "" && CredentialFormat(f.value) != "" {
					found = append(found, f)
				}
			}
			continue
		}
		found = append(found, secretsInBody(b.Body, prefix+segment+".")...)
	}
	return found
}

// IsReferenceType reports whether the type of a name/value pair says its
// value names a secret kept elsewhere, as a CodeBuild environment
// variable of type PARAMETER_STORE or SECRETS_MANAGER does.
func IsReferenceType(t string) bool {
	switch strings.ToUpper(t) {
	case "PARAMETER_STORE", "SECRETS_MANAGER", "SECRET_MANAGER", "SECRETSMANAGER":
		return true
	}
	return false
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
		value := hclfile.StringLiteral(e)
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
		if reason := Reason(keyName, value, freeForm && !tags); reason != "" {
			return []foundSecret{{attribute: path, key: key, value: value, reason: reason, literal: e, whole: true}}
		}
		return nil
	case *hclsyntax.ObjectConsExpr:
		values := map[string]string{}
		for _, item := range e.Items {
			if k := hclfile.ObjectKey(item.KeyExpr); k != "" {
				values[k] = hclfile.StringLiteral(item.ValueExpr)
			}
		}
		pairName := firstOf(values, "name", "Name", "key", "Key")
		reference := IsReferenceType(firstOf(values, "type", "Type"))
		var found []foundSecret
		for _, item := range e.Items {
			k := hclfile.ObjectKey(item.KeyExpr)
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
			if reason := Reason(keyName, v, true); reason != "" {
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
// says it is a secret (see Reason) to a value of its own, not one it
// reads from elsewhere, as "$DB_PASSWORD" or "$(aws secretsmanager ...)"
// does. A script in base64 is decoded first.
func scriptSecret(value string) string {
	text := value
	if v := strings.TrimSpace(value); len(v)%4 == 0 && base64Text.MatchString(v) {
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil && utf8.Valid(decoded) {
			text = string(decoded)
		}
	}
	if name := CredentialFormat(text); name != "" {
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
				if IsName(key) && scriptValue(v) && Reason(key, v, true) != "" {
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
