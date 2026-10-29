// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// An API that isn't enabled is told by its 403's ErrorInfo detail or, in
// the older format, by an error item, and leaves nothing to list; a 403
// for anything else, or another status with those reasons, is a failure.
// A 404 is a resource gone, and a 400 a location the API doesn't know.
func TestGoogleCodes(t *testing.T) {
	t.Parallel()
	errorInfo := func(reason string) []interface{} {
		return []interface{}{map[string]interface{}{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason, "domain": "googleapis.com"}}
	}
	for _, tc := range []struct {
		name string
		err  error
		want harvest.Outcome
	}{
		{"ErrorInfo", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("SERVICE_DISABLED")}, harvest.OutcomeNothingToList},
		{"older format", &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "accessNotConfigured"}}}, harvest.OutcomeNothingToList},
		{"wrapped", fmt.Errorf("region: %w", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("SERVICE_DISABLED")}), harvest.OutcomeNothingToList},
		{"missing permission", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("IAM_PERMISSION_DENIED")}, harvest.OutcomeFail},
		{"reason only in the message", &googleapi.Error{Code: http.StatusForbidden, Message: "SERVICE_DISABLED"}, harvest.OutcomeFail},
		{"not a 403", &googleapi.Error{Code: http.StatusTooManyRequests, Details: errorInfo("SERVICE_DISABLED")}, harvest.OutcomeFail},
		{"not found", &googleapi.Error{Code: http.StatusNotFound}, harvest.OutcomeGone},
		{"unknown location", &googleapi.Error{Code: http.StatusBadRequest}, harvest.OutcomeSkipRegion},
		{"conflict", &googleapi.Error{Code: http.StatusConflict}, harvest.OutcomeFail},
		{"not an API error", errors.New("connection refused"), harvest.OutcomeFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := googleCodes.Classify(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
