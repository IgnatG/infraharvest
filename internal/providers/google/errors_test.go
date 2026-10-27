// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"
)

// An API that isn't enabled is told by its 403's ErrorInfo detail or, in
// the older format, by an error item; a 403 for anything else, or another
// status with those reasons, isn't it. nothingToList counts it as nothing
// there, as it does a 404 or a 400, but not a missing permission.
func TestServiceDisabledAndNothingToList(t *testing.T) {
	errorInfo := func(reason string) []interface{} {
		return []interface{}{map[string]interface{}{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason, "domain": "googleapis.com"}}
	}
	for _, tc := range []struct {
		name                    string
		err                     error
		disabled, nothingToList bool
	}{
		{"ErrorInfo", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("SERVICE_DISABLED")}, true, true},
		{"older format", &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "accessNotConfigured"}}}, true, true},
		{"wrapped", fmt.Errorf("region: %w", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("SERVICE_DISABLED")}), true, true},
		{"missing permission", &googleapi.Error{Code: http.StatusForbidden, Details: errorInfo("IAM_PERMISSION_DENIED")}, false, false},
		{"reason only in the message", &googleapi.Error{Code: http.StatusForbidden, Message: "SERVICE_DISABLED"}, false, false},
		{"not a 403", &googleapi.Error{Code: http.StatusTooManyRequests, Details: errorInfo("SERVICE_DISABLED")}, false, false},
		{"not found", &googleapi.Error{Code: http.StatusNotFound}, false, true},
		{"unknown location", &googleapi.Error{Code: http.StatusBadRequest}, false, true},
		{"not an API error", errors.New("connection refused"), false, false},
	} {
		if got := isServiceDisabled(tc.err); got != tc.disabled {
			t.Errorf("%s: isServiceDisabled = %v, want %v", tc.name, got, tc.disabled)
		}
		if got := nothingToList(tc.err); got != tc.nothingToList {
			t.Errorf("%s: nothingToList = %v, want %v", tc.name, got, tc.nothingToList)
		}
	}
}
