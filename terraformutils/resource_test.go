// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package terraformutils

import (
	"reflect"
	"testing"
)

func TestAttributeTags(t *testing.T) {
	got := AttributeTags(map[string]string{
		"id":                               "vpc-1",
		"tags.%":                           "2",
		"tags.Name":                        "main",
		"tags.aws:cloudformation:stack-id": "arn:stack",
		"labels.env":                       "prod",
		"tag.#":                            "1",
		"tags.#":                           "1",
		"tags.0.key":                       "listed",
	})
	want := map[string]string{"Name": "main", "aws:cloudformation:stack-id": "arn:stack", "env": "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := AttributeTags(map[string]string{"id": "x"}); got != nil {
		t.Errorf("no tags: got %v, want nil", got)
	}
}
