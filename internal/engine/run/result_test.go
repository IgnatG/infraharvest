// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

func TestResultWith(t *testing.T) {
	t.Parallel()
	previous := &Result{Imported: []rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}}, Gate: gate.Gate{{Name: gate.CheckFormat}}}
	added := &Result{Imported: []rootfile.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}}, Gate: gate.Gate{{Name: gate.CheckFormat, Passed: true}}}
	got := previous.With(added)
	if len(got.Imported) != 2 || !got.Gate.Passed() || len(previous.Imported) != 1 {
		t.Errorf("got %+v", got)
	}
	var none *Result
	if none.With(added) != added {
		t.Error("without an earlier result: want what was added")
	}
}
