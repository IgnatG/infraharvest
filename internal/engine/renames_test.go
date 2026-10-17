// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// renameSource has references in nested blocks, in templates, in index
// keys, after a dot (data.aws_vpc.v_0.id is no reference to aws_vpc.v_0),
// and to attributes no rename is for.
const renameSource = `resource "aws_subnet" "a" {
  vpc_id = aws_vpc.v_0.id
  tags = {
    Name = "in ${aws_vpc.v_3.arn}"
    data = data.aws_vpc.v_0.id
  }
  dynamic "x" {
    content {
      cidr = lookup(local.cidrs, aws_vpc.v_3.cidr_block, aws_vpc.v_0.owner_id)
    }
  }
}

resource "aws_security_group" "b" {
  vpc_id = aws_vpc.v_6.name
  rules  = [for r in aws_vpc.v_0.ids : r]
  other  = var.x[aws_vpc.v_3.bucket]
}
`

// TestRenameIndex checks that the renames an index hands each body leave
// it as all the renames would, in their order, including one whose search
// has fewer than two steps.
func TestRenameIndex(t *testing.T) {
	t.Parallel()
	parse := func() *hclwrite.File {
		f, diags := hclwrite.ParseConfig([]byte(renameSource), GeneratedFileName, hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		return f
	}
	references := append(benchReferenceRenames(t, 9), referenceRename{[]string{"local"}, hclwrite.TokensForIdentifier("locals")})
	prefixes := append(benchPrefixRenames(9), prefixRename{[]string{"var"}, []string{"variable"}})

	all, indexed := parse(), parse()
	byStart := indexRenames(len(references), func(i int) []string { return references[i].search })
	for i, block := range all.Body().Blocks() {
		for _, rn := range references {
			replaceReferences(block.Body(), rn.search, rn.replacement)
		}
		replaceAllReferences(indexed.Body().Blocks()[i].Body(), references, byStart)
	}
	if got, want := string(indexed.Bytes()), string(all.Bytes()); got != want {
		t.Errorf("replaceAllReferences gave\n%s\nwant\n%s", got, want)
	}
	if string(all.Bytes()) == renameSource {
		t.Error("no reference was replaced")
	}

	all, indexed = parse(), parse()
	byStart = indexRenames(len(prefixes), func(i int) []string { return prefixes[i].search })
	for i, block := range all.Body().Blocks() {
		for _, rn := range prefixes {
			renameIn(block.Body(), rn.search, rn.replacement)
		}
		renameAll(indexed.Body().Blocks()[i].Body(), prefixes, byStart)
	}
	if got, want := string(indexed.Bytes()), string(all.Bytes()); got != want {
		t.Errorf("renameAll gave\n%s\nwant\n%s", got, want)
	}
	if string(all.Bytes()) == renameSource {
		t.Error("no reference was renamed")
	}
}
