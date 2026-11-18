// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"

	"github.com/IgnatG/infraharvest/internal/providers/providertest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// sentTo sends one GET through the pipeline azcore builds from the retry
// settings the provider hands its clients, with the delay between retries
// cut to a millisecond, and returns the answer's status and how many
// requests the cloud saw.
func sentTo(t *testing.T, cloud *providertest.Cloud) (int, int) {
	t.Helper()
	retry := retryOptions()
	retry.RetryDelay = time.Millisecond
	pipeline := runtime.NewPipeline("test", "v0.0.0", runtime.PipelineOptions{}, &policy.ClientOptions{
		Retry:     retry,
		Transport: cloud,
	})
	req, err := runtime.NewRequest(t.Context(), http.MethodGet, "https://management.azure.com/subscriptions/sub/resourceGroups")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pipeline.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, len(cloud.Requests())
}

// A throttled call (429) and a transient failure (408, 500, 502, 503, 504)
// are sent again until the budget every cloud has is spent, then fail with
// the last answer.
func TestRetriesThrottlingAndTransientFailures(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		cloud := providertest.New(func(providertest.Request) providertest.Answer {
			return providertest.Status(status, `{"error":{"code":"Throttled"}}`)
		})
		got, requests := sentTo(t, cloud)
		if got != status || requests != ratelimit.MaxAttempts {
			t.Errorf("%d: got %d after %d requests, want the same after %d", status, got, requests, ratelimit.MaxAttempts)
		}
	}
}

// A call that passes after being throttled returns its answer, and one the
// caller can't fix by waiting is sent once.
func TestRetriesStopWhenTheCallPassesOrCannotPass(t *testing.T) {
	t.Parallel()
	var n int
	cloud := providertest.New(func(providertest.Request) providertest.Answer {
		n++
		if n < 4 {
			return providertest.Status(http.StatusTooManyRequests, `{}`)
		}
		return providertest.JSON(`{"value":[]}`)
	})
	if got, requests := sentTo(t, cloud); got != http.StatusOK || requests != 4 {
		t.Errorf("throttled thrice: got %d after %d requests, want 200 after 4", got, requests)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound} {
		cloud := providertest.New(func(providertest.Request) providertest.Answer {
			return providertest.Status(status, `{}`)
		})
		if got, requests := sentTo(t, cloud); got != status || requests != 1 {
			t.Errorf("%d: got %d after %d requests, want one", status, got, requests)
		}
	}
}

// The clients the provider signs in get the retry settings.
func TestRetryOptionsAreTheBudgetOfEveryCloud(t *testing.T) {
	t.Parallel()
	if got := retryOptions().MaxRetries; int(got)+1 != ratelimit.MaxAttempts {
		t.Errorf("MaxRetries %d, want one fewer than %d attempts", got, ratelimit.MaxAttempts)
	}
}
