// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/magodo/azlist/azlist"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A list refused, throttled, timed out or failed by Resource Manager, or
// without an answer, left resources out; a 404 is a resource gone; the
// other answers, such as a child type a resource doesn't have, or an item
// azlist couldn't read, leave nothing out. The status is read alike from
// an SDK response and from the message azlist keeps of one.
func TestAzureCodes(t *testing.T) {
	t.Parallel()
	listError := func(message string) error {
		return fmt.Errorf("azurerm: resource_graph: %w", azlist.ListError{Endpoint: "/X/CONFIG", Version: "2024-04-01", Message: message})
	}
	response := func(status int) string {
		return "GET https://management.azure.com/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app/config\n" +
			"--------------------------------------------------------------------------------\n" +
			"RESPONSE " + strconv.Itoa(status) + ": " + http.StatusText(status) + "\nERROR CODE: X\n"
	}
	for status, want := range map[int]harvest.Outcome{
		http.StatusUnauthorized:        harvest.OutcomeFail,
		http.StatusForbidden:           harvest.OutcomeFail,
		http.StatusRequestTimeout:      harvest.OutcomeFail,
		http.StatusTooManyRequests:     harvest.OutcomeFail,
		http.StatusInternalServerError: harvest.OutcomeFail,
		http.StatusServiceUnavailable:  harvest.OutcomeFail,
		http.StatusNotFound:            harvest.OutcomeGone,
		http.StatusBadRequest:          harvest.OutcomeNothingToList,
		http.StatusMethodNotAllowed:    harvest.OutcomeNothingToList,
		http.StatusConflict:            harvest.OutcomeNothingToList,
		http.StatusNotImplemented:      harvest.OutcomeNothingToList,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			if got := azureCodes.Classify(&azcore.ResponseError{StatusCode: status}); got != want {
				t.Errorf("response: got %v, want %v", got, want)
			}
			if got := azureCodes.Classify(listError(response(status))); got != want {
				t.Errorf("azlist: got %v, want %v", got, want)
			}
		})
	}
	for name, tc := range map[string]struct {
		err  error
		want harvest.Outcome
	}{
		"no answer":                 {errors.New("connection reset"), harvest.OutcomeFail},
		"azlist, no answer":         {listError("Get \"https://management.azure.com/...\": dial tcp: i/o timeout"), harvest.OutcomeFail},
		"unreadable item":           {listError("no resource id found in response: {}"), harvest.OutcomeNothingToList},
		"unparsable id":             {listError("parsing resource id \"x\": invalid"), harvest.OutcomeNothingToList},
		"shape text, not ListError": {errors.New("marshalling x"), harvest.OutcomeFail},
	} {
		if got := azureCodes.Classify(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}
