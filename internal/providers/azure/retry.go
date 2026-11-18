// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// retryOptions are the retry settings of the Azure clients: as many
// attempts as the other clouds' (see ratelimit.MaxAttempts), where azcore
// retries three times. MaxRetries counts the retries after the first
// attempt. The delays stay azcore's, 800ms doubling up to 60s, and its
// retry honours a Retry-After. The status codes are azcore's default
// list, named here so that the throttling (429) and the transient
// failures (408 and 500, 502, 503, 504) stay retried, whatever a later
// azcore changes.
func retryOptions() policy.RetryOptions {
	return policy.RetryOptions{
		MaxRetries: ratelimit.MaxAttempts - 1,
		StatusCodes: []int{
			http.StatusRequestTimeout,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
		},
	}
}
