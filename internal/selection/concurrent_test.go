// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"sync"
	"testing"
)

// The accounts of one import decide in parallel, from one file.
func TestDecideConcurrently(t *testing.T) {
	f := &File{Version: Version, Resources: []Resource{
		{Scope: "aws/1/eu-west-2", Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Scope: "aws/2/eu-west-2", Type: "aws_vpc", ID: "vpc-2"},
	}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !f.HasIn("aws/1/eu-west-2", "aws_vpc", "vpc-1") || !f.DecideIn("aws/1/eu-west-2", "aws_vpc", "vpc-1", "main", nil).Include {
				t.Error("vpc-1 is included")
			}
			if f.DecideIn("aws/2/eu-west-2", "aws_vpc", "vpc-2", "main", nil).Include {
				t.Error("vpc-2 is excluded")
			}
		}()
	}
	wg.Wait()
}
