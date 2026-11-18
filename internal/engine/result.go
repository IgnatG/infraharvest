// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// redactResult replaces the values of secrets in the text of result that
// came from plans, which WriteReadme and the report show: the gate's
// details, why module calls were declined, the errors of what was left out
// and the warnings. Plans redact their own output already (see
// secret.RedactingTerraform); this also covers text made before a value was
// hidden.
func redactResult(result *Result, secrets []secret.Variable) {
	forms := secret.TextForms(secret.KnownValues(secrets))
	if len(forms) == 0 {
		return
	}
	for i := range result.Gate {
		for j, d := range result.Gate[i].Details {
			result.Gate[i].Details[j] = secret.RedactText(d, forms)
		}
	}
	for i := range result.Modules {
		result.Modules[i].Declined = secret.RedactText(result.Modules[i].Declined, forms)
	}
	for i := range result.Rejected {
		for j, e := range result.Rejected[i].Errors {
			result.Rejected[i].Errors[j] = secret.RedactText(e, forms)
		}
	}
	for i, w := range result.Warnings {
		result.Warnings[i] = secret.RedactText(w, forms)
	}
}
