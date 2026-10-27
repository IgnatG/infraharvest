// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A command only a build with AWS has, such as cfn, is compiled out of a
// build without it: the file that adds it to awsCommands carries the
// build constraint the AWS provider command does.
func TestAWSCommandsHaveBuildConstraint(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "awsCommands = append(") {
			continue
		}
		found = true
		if !strings.Contains(string(src), "\n//go:build !minimal || aws\n") {
			t.Errorf("%s: missing //go:build !minimal || aws", f)
		}
	}
	if !found {
		t.Error("no file adds an AWS command, but cfn is one")
	}
}
