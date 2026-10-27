// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"net/http"

	"google.golang.org/api/googleapi"
)

// nothingToList reports whether a failed list means there is nothing
// there rather than that listing failed: the API isn't enabled in the
// project (see isServiceDisabled), or the location doesn't offer the
// service (404, or 400 for a location the API doesn't know). A 403 for a
// missing permission is a failure.
func nothingToList(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case http.StatusNotFound, http.StatusBadRequest:
		return true
	}
	return isServiceDisabled(err)
}

// isServiceDisabled reports whether err says the API isn't enabled in the
// project: a 403 whose reason is SERVICE_DISABLED, in its
// google.rpc.ErrorInfo detail, or accessNotConfigured, in one of its error
// items in the older format.
func isServiceDisabled(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusForbidden {
		return false
	}
	for _, item := range apiErr.Errors {
		if item.Reason == "accessNotConfigured" {
			return true
		}
	}
	for _, detail := range apiErr.Details {
		if info, ok := detail.(map[string]interface{}); ok && info["reason"] == "SERVICE_DISABLED" {
			return true
		}
	}
	return false
}
