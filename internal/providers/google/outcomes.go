// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"net/http"
	"strconv"

	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// googleCodes is what Google Cloud's API answers mean for a listing (see
// harvest.Codes):
//
//   - Nothing to list: the API isn't enabled in the project, a 403 whose
//     reason is SERVICE_DISABLED, in its google.rpc.ErrorInfo detail, or
//     accessNotConfigured, in one of its error items in the older format.
//   - The resource is gone (404), as an instance deleted since it was
//     listed; a list in a location that doesn't offer the service answers
//     so too.
//   - The region doesn't offer the service: a 400 for a location the API
//     doesn't know.
//
// Any other answer, a 403 for a missing permission included, is a failure.
var googleCodes = &harvest.Codes{
	Of: googleErrorCodes,
	Exact: map[string]harvest.Outcome{
		"403 SERVICE_DISABLED":    harvest.OutcomeNothingToList,
		"403 accessNotConfigured": harvest.OutcomeNothingToList,
		"404":                     harvest.OutcomeGone,
		"400":                     harvest.OutcomeSkipRegion,
	},
}

// googleErrorCodes returns the codes of err for googleCodes: the reasons
// of a 403, from its error items and its ErrorInfo details, each after
// the status, as in "403 SERVICE_DISABLED", then the status alone; none
// for an error that isn't the API's answer.
func googleErrorCodes(err error) []string {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return nil
	}
	status := strconv.Itoa(apiErr.Code)
	var codes []string
	if apiErr.Code == http.StatusForbidden {
		for _, item := range apiErr.Errors {
			codes = append(codes, status+" "+item.Reason)
		}
		for _, detail := range apiErr.Details {
			if info, ok := detail.(map[string]interface{}); ok {
				if reason, ok := info["reason"].(string); ok {
					codes = append(codes, status+" "+reason)
				}
			}
		}
	}
	return append(codes, status)
}
