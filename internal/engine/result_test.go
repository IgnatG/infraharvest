// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"reflect"
	"testing"
)

func TestResultBlocks(t *testing.T) {
	addressed := &Result{
		Imported: []Import{{Type: "aws_s3_bucket", Name: "logs", ID: "logs"}, {Type: "aws_sqs_queue", Name: "orders", ID: "q1"}},
		Addresses: []Managed{
			{Type: "aws_s3_bucket", ID: "logs", Address: `module.logs["a.b"].aws_s3_bucket.this[0]`},
			{Type: "aws_sqs_queue", ID: "q1", Address: `aws_sqs_queue.orders["a"]`},
		},
	}
	want := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs"},
		{Type: "aws_sqs_queue", ID: "q1", Address: "aws_sqs_queue.orders"},
	}
	if got := addressed.Blocks(); !reflect.DeepEqual(got, want) {
		t.Errorf("with addresses: got %+v, want %+v", got, want)
	}
	// An older checkpoint's result has only the labels.
	labelled := &Result{Imported: []Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q1"}}}
	if got := labelled.Blocks(); !reflect.DeepEqual(got, []Managed{{Type: "aws_sqs_queue", ID: "q1", Address: "aws_sqs_queue.orders"}}) {
		t.Errorf("without addresses: got %+v", got)
	}
}

func TestResultWithKept(t *testing.T) {
	kept := []Import{{Type: "aws_sqs_queue", Name: "audit", ID: "q3"}}
	r := &Result{
		Imported:  []Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q1"}},
		Addresses: []Managed{{Type: "aws_sqs_queue", ID: "q1", Address: "aws_sqs_queue.orders"}},
	}
	with := r.WithKept(kept)
	if want := []Import{r.Imported[0], kept[0]}; !reflect.DeepEqual(with.Imported, want) {
		t.Errorf("imported: got %+v, want %+v", with.Imported, want)
	}
	if want := []Managed{r.Addresses[0], {Type: "aws_sqs_queue", ID: "q3", Address: "aws_sqs_queue.audit"}}; !reflect.DeepEqual(with.Addresses, want) {
		t.Errorf("addresses: got %+v, want %+v", with.Addresses, want)
	}
	if len(r.Imported) != 1 || len(r.Addresses) != 1 {
		t.Errorf("r changed: %+v", r)
	}
	// A result without addresses keeps none: its labels tell them all.
	if with := (&Result{Imported: r.Imported}).WithKept(kept); len(with.Addresses) != 0 || len(with.Imported) != 2 {
		t.Errorf("without addresses: got %+v", with)
	}
}
