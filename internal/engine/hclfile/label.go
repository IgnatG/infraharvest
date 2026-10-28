// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"regexp"
	"slices"
	"strings"
)

var notInLabel = regexp.MustCompile(`[^a-z0-9]+`)

// wordBoundaries are the word boundaries inside a camelCase or PascalCase
// name: the end of an acronym before a capitalised word (HTTPApi), a
// lower-case letter before an upper-case one (allowHttp), and a digit
// before a capitalised word (Ipv4Address).
var wordBoundaries = []*regexp.Regexp{
	regexp.MustCompile(`([A-Z])([A-Z][a-z])`),
	regexp.MustCompile(`([a-z])([A-Z])`),
	regexp.MustCompile(`([0-9])([A-Z][a-z])`),
}

// Label turns a resource's name into a resource block label: snake_case,
// with the words of a camelCase name kept apart (AllowHttpApi becomes
// allow_http_api), and a run of words that repeats the run before it
// written once (shop_shop_web becomes shop_web). It puts "r_" in front if
// the label would start with a digit, and gives "resource" if nothing is
// left.
func Label(name string) string {
	label := strings.Join(LabelWords(name), "_")
	switch {
	case label == "":
		return "resource"
	case StartsWithDigit(label):
		return "r_" + label
	}
	return label
}

// LabelWords returns the lower-case words of name that Label joins.
func LabelWords(name string) []string {
	for _, boundary := range wordBoundaries {
		name = boundary.ReplaceAllString(name, "${1}_${2}")
	}
	words := strings.FieldsFunc(notInLabel.ReplaceAllString(strings.ToLower(name), "_"), func(r rune) bool { return r == '_' })
	return withoutRepeats(words)
}

// withoutRepeats drops each run of words that repeats the run right before
// it, longest runs first, so that a name made of a parent's name and a
// child's that starts with it (cluster shop, service shop-web) says it
// once. Runs of numbers only are kept, as in an IP address (10_0_0_1).
func withoutRepeats(words []string) []string {
	for n := len(words) / 2; n >= 1; n-- {
		for i := 0; i+2*n <= len(words); {
			run := words[i : i+n]
			if slices.Equal(run, words[i+n:i+2*n]) && !slices.ContainsFunc(run, IsNumber) {
				words = slices.Delete(words, i+n, i+2*n)
				continue
			}
			i++
		}
	}
	return words
}

// IsNumber reports whether word is digits only.
func IsNumber(word string) bool {
	return word != "" && strings.Trim(word, "0123456789") == ""
}

// StartsWithDigit reports whether s starts with a digit, which a label
// can't.
func StartsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}
