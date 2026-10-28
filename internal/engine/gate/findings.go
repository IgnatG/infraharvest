// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
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
	files := map[string]*hclfile.File{}
	blockAt := func(at position) string {
		if at.file == "" || at.line <= 0 || !filepath.IsLocal(filepath.FromSlash(at.file)) {
			return ""
		}
		f, ok := files[at.file]
		if !ok {
			if src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(at.file))); err == nil {
				f, _ = hclfile.Parse(at.file, src)
			}
			files[at.file] = f
		}
		if f == nil {
			return ""
		}
		return blockAt(f, at.line)
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
		case hclfile.NamesModuleCall(f.Resource) && strings.Contains(f.at.file, "/"):
			if inner := blockAt(f.at); inner != "" {
				f.Resource += "." + inner
			}
		}
		f.at = position{}
	}
}

// blockAt returns the address of the top-level block of f spanning line: a
// resource's, a data source's or a module call's, or, for an import
// block, the address it imports into; "" for any other.
func blockAt(f *hclfile.File, line int) string {
	for _, b := range f.Syntax.Blocks {
		if !hclfile.Spans(b.Range(), line) {
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
				return addressOf(strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.Src))))
			}
		}
		return ""
	}
	return ""
}
