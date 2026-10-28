// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

var lambdaPlaceholder = map[string]Placeholder{"aws_lambda_function": {
	OneOf:  []string{"filename", "image_uri", "s3_bucket"},
	Set:    map[string]string{"filename": "lambda/{function_name}.zip"},
	Ignore: []string{"filename", "source_code_hash"},
	Reason: "the code is deployed outside Terraform",
}}

// A zip function, which names no code, gets a placeholder that Terraform
// ignores; an image function, which names its image, doesn't.
func TestSetPlaceholders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_lambda_function" "zip" {
  filename      = null
  function_name = "orders"
  image_uri     = null
}

resource "aws_lambda_function" "image" {
  function_name = "web"
  image_uri     = "123456789012.dkr.ecr.eu-west-2.amazonaws.com/web:1"
}
`)
	changed, err := setPlaceholders(f.Path, lambdaPlaceholder)
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}
	got := readFile(t, dir, rootfile.GeneratedFileName)
	want := `resource "aws_lambda_function" "zip" {
  filename      = "lambda/orders.zip"
  function_name = "orders"
  image_uri     = null
  lifecycle {
    # The code is deployed outside Terraform.
    ignore_changes = [filename, source_code_hash]
  }
}
`
	if !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", got, want)
	}
	_, image, found := strings.Cut(got, `"image"`)
	if !found || strings.Contains(image, "lifecycle") || strings.Contains(image, "filename") {
		t.Errorf("the image function got a placeholder:\n%s", image)
	}
	if changed, err := setPlaceholders(f.Path, lambdaPlaceholder); err != nil || changed {
		t.Errorf("setting them again: changed=%v err=%v", changed, err)
	}
}
