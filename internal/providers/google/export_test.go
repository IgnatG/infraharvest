// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"

	"github.com/IgnatG/infraharvest/internal/runstate"
)

// What only the tests use.

// limitedBy returns base limited by run, made the first time the run asks
// (see limitedClient), or nil for no base.
func limitedBy(run *runstate.Run, base *http.Client) *http.Client {
	return limitedByKey(run, "", base)
}
