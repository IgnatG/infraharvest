// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// The default resources the DocumentDB, Redshift and CloudWatch listers
// used to leave out in code are skipped by the catalogue instead, with a
// reason selection.yaml shows, and nothing else of their types is.
func TestDefaultsSkippedByTheCatalogue(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	for _, tt := range []struct {
		typ, id    string
		attributes map[string]string
		skipped    bool
	}{
		{"aws_docdb_subnet_group", "default", nil, true},
		{"aws_docdb_subnet_group", "docs", nil, false},
		{"aws_docdb_subnet_group", "default-docs", nil, false},
		{"aws_docdb_cluster_parameter_group", "default.docdb5.0", nil, true},
		{"aws_docdb_cluster_parameter_group", "docs-audit", nil, false},
		{"aws_docdb_cluster_parameter_group", "default-audit", nil, false},
		{"aws_redshift_parameter_group", "default.redshift-1.0", nil, true},
		{"aws_redshift_parameter_group", "etl-params", nil, false},
		{"aws_cloudwatch_event_rule", "StepFunctionsGetEventsForStepFunctionsExecutionRule", map[string]string{"managed_by": "states.amazonaws.com"}, true},
		{"aws_cloudwatch_event_rule", "nightly", map[string]string{}, false},
	} {
		reason := cat.Skipped(tt.typ, tt.id, tt.attributes)
		if (reason != "") != tt.skipped {
			t.Errorf("%s %s: skipped with %q, want skipped %v", tt.typ, tt.id, reason, tt.skipped)
		}
	}
}
