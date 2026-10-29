// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/magodo/azlist/azlist"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// codeUnreadableItem is azureCodes' code for an item of a list azlist
// couldn't read (see shapeErrors), which has no HTTP status.
const codeUnreadableItem = "unreadable item"

// azureCodes is what Resource Manager's answers mean for a listing (see
// harvest.Codes), by HTTP status:
//
//   - A failure, as resources were left out: a list refused (401, 403),
//     timed out (408), throttled (429) or failed by Resource Manager
//     (5xx), or one that got no answer at all.
//   - The resource is gone (404), as when it was deleted since it was
//     listed.
//   - Nothing to list: the other answers, such as 400 or 409 for a child
//     type a resource's SKU or state doesn't have, 405 and 501 for types
//     that can't be listed, and items azlist couldn't read, which azurerm
//     has no type to import by either.
var azureCodes = &harvest.Codes{
	Of: azureErrorCodes,
	Exact: map[string]harvest.Outcome{
		"401":              harvest.OutcomeFail,
		"403":              harvest.OutcomeFail,
		"404":              harvest.OutcomeGone,
		"408":              harvest.OutcomeFail,
		"429":              harvest.OutcomeFail,
		"501":              harvest.OutcomeNothingToList,
		codeUnreadableItem: harvest.OutcomeNothingToList,
	},
	Patterns: []harvest.CodePattern{
		{Pattern: "5??", Outcome: harvest.OutcomeFail},
		{Pattern: "[1-4]??", Outcome: harvest.OutcomeNothingToList},
	},
}

// listStatus is the HTTP status of a failed list in the message of an
// azlist.ListError, as the Azure SDK's ResponseError writes it: azlist
// keeps only the message, so the status is read from its text.
var listStatus = regexp.MustCompile(`RESPONSE (\d{3}):`)

// shapeErrors start the messages of the azlist.ListErrors for an item of
// a list that it couldn't read, such as one without an ID.
var shapeErrors = []string{"marshalling ", "unmarshalling ", "no resource id found", "resource id is not a string", "parsing resource id "}

// azureErrorCodes returns the codes of err for azureCodes: the HTTP
// status of an azcore.ResponseError, or of an azlist.ListError, read from
// its message (see listStatus); codeUnreadableItem for a ListError of an
// item azlist couldn't read; none for an error without an answer.
func azureErrorCodes(err error) []string {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return []string{strconv.Itoa(respErr.StatusCode)}
	}
	var listErr azlist.ListError
	if !errors.As(err, &listErr) {
		return nil
	}
	if m := listStatus.FindStringSubmatch(listErr.Message); m != nil {
		return []string{m[1]}
	}
	if slices.ContainsFunc(shapeErrors, func(prefix string) bool { return strings.HasPrefix(listErr.Message, prefix) }) {
		return []string{codeUnreadableItem}
	}
	return nil
}
