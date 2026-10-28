// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// position is where in the directory a scanner found something: a file,
// relative to the directory, and a line of it.
type position struct {
	file string
	line int
}

// terraformAddress matches the address of a resource, a data source or a
// module call, in module calls or not, with instance keys: what a
// scanner names as the resource a finding is about, which a scanner of
// other files, such as checkov's secrets scan, names otherwise.
var terraformAddress = regexp.MustCompile(`^(module\.[A-Za-z_][\w-]*(\[[^\]]*\])?\.)*((data\.)?[a-z][a-z0-9]*_[a-z0-9_]+\.[A-Za-z_][\w-]*(\[[^\]]*\])?|module\.[A-Za-z_][\w-]*(\[[^\]]*\])?)$`)

// leadingAddress matches a message that starts with a resource's
// address, such as "aws_s3_bucket.logs has no owner tag" or
// "aws_s3_bucket.logs: no versioning", as a policy's message may name the
// resource it is about.
var leadingAddress = regexp.MustCompile(`^((module\.[A-Za-z_][\w-]*(\[[^\]\s]*\])?\.)*(data\.)?[a-z][a-z0-9]*_[a-z0-9_]+\.[A-Za-z_][\w-]*(\[[^\]\s]*\])?)(:|\s|$)`)

// addressOf returns s if it is a Terraform address (see
// terraformAddress), or "".
func addressOf(s string) string {
	if terraformAddress.MatchString(s) {
		return s
	}
	return ""
}

// messageAddress returns the address a message starts with, or "".
func messageAddress(message string) string {
	if m := leadingAddress.FindStringSubmatch(message); m != nil {
		return m[1]
	}
	return ""
}

// locateFindings sets on each finding the scanner gave a position but no
// resource the address of the block of dir's configuration at that
// position: a resource, data source or module call, or the resource an
// import block imports into. A finding a scanner places in a module
// call, by the call alone, such as trivy's module.logs at a line of
// modules/bucket/main.tf, is placed at the resource of the module at that
// line, in the call: module.logs.aws_s3_bucket.this. A finding at no
// such block, such as one about a variable or the provider, stays
// about none. Each file is parsed once; one that doesn't parse, or isn't
// in dir, places nothing.
func locateFindings(dir string, findings []Finding) {
	files := map[string]*hclFile{}
	blockAt := func(at position) string {
		if at.file == "" || at.line <= 0 || !filepath.IsLocal(filepath.FromSlash(at.file)) {
			return ""
		}
		f, ok := files[at.file]
		if !ok {
			if src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(at.file))); err == nil {
				f, _ = parseHCL(at.file, src)
			}
			files[at.file] = f
		}
		if f == nil {
			return ""
		}
		return f.blockAt(at.line)
	}
	for i := range findings {
		f := &findings[i]
		switch {
		case f.Resource == "":
			// Only in the root's own files: in a module's, the block's
			// address is the module's, not the root's.
			if !strings.Contains(f.at.file, "/") {
				f.Resource = blockAt(f.at)
			}
		case namesModuleCall(f.Resource) && strings.Contains(f.at.file, "/"):
			if inner := blockAt(f.at); inner != "" {
				f.Resource += "." + inner
			}
		}
		f.at = position{}
	}
}

// namesModuleCall reports whether address is a module call's, in module
// calls or not, rather than a resource's.
func namesModuleCall(address string) bool {
	names := addressNames(address)
	return len(names) >= 2 && names[len(names)-2] == "module"
}

// blockAt returns the address of the top-level block spanning line: a
// resource's, a data source's or a module call's, or, for an import
// block, the address it imports into; "" for any other.
func (f *hclFile) blockAt(line int) string {
	for _, b := range f.syntax.Blocks {
		if !spans(b.Range(), line) {
			continue
		}
		switch {
		case b.Type == "resource" && len(b.Labels) == 2:
			return b.Labels[0] + "." + b.Labels[1]
		case b.Type == "data" && len(b.Labels) == 2:
			return "data." + b.Labels[0] + "." + b.Labels[1]
		case b.Type == "module" && len(b.Labels) == 1:
			return "module." + b.Labels[0]
		case b.Type == "import":
			if to, ok := b.Body.Attributes["to"]; ok {
				return addressOf(strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.src))))
			}
		}
		return ""
	}
	return ""
}

// ResourceAt returns the resource of managed, what a root imports, that a
// finding about address is about: the one at address; or else the one
// at address but for instance keys, or the one module call address
// makes, where there is only one. A finding about a module call of
// several resources, or about none the root imports, is about none.
func ResourceAt(managed []Managed, address string) (Managed, bool) {
	if address == "" {
		return Managed{}, false
	}
	for _, m := range managed {
		if m.Address == address {
			return m, true
		}
	}
	bare := strings.Join(addressNames(address), ".")
	one := func(match func(string) bool) (Managed, bool) {
		var found []Managed
		for _, m := range managed {
			if match(strings.Join(addressNames(m.Address), ".")) {
				found = append(found, m)
			}
		}
		if len(found) == 1 {
			return found[0], true
		}
		return Managed{}, false
	}
	if m, ok := one(func(a string) bool { return a == bare }); ok {
		return m, true
	}
	if namesModuleCall(address) {
		return one(func(a string) bool { return strings.HasPrefix(a, bare+".") })
	}
	return Managed{}, false
}
