// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secretdetect

import (
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	urlPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S+$`)
	pathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._@:-]+){2,}/?$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexPattern  = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9+/=_-]+$`)
	// sidPattern is what IAM takes as a policy statement's Sid.
	sidPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)
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
	case len(v) < MinSecretLength, plainWords[strings.ToLower(v)]:
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
			if IsName(k) || strings.EqualFold(k, "sig") || strings.EqualFold(k, "key") || strings.EqualFold(k, "code") {
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

// IsName reports whether key names a secret, such as API_SECRET,
// DB_PASSWORD or clientSecret, but not SECRET_ARN or ACCESS_KEY_ID.
func IsName(key string) bool {
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
