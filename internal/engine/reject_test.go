// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"reflect"
	"slices"
	"testing"
)

// An import Terraform generated no configuration for, such as of a KMS key
// scheduled for deletion, is found.
func TestUnconfigured(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, "resource \"aws_kms_key\" \"live\" {\n}\n")
	content, err := ImportsFile([]Import{{Type: "aws_kms_key", Name: "live", ID: "k-1"}, {Type: "aws_kms_key", Name: "pending", ID: "k-2"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))

	missing, err := unconfigured(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for addr := range missing {
		got = append(got, addr)
	}
	slices.Sort(got)
	if want := []string{"aws_kms_key.pending"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
