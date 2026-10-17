// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// countingCredential returns tokens that expire after lifetime, counting
// the requests, and fails with err when it is set.
type countingCredential struct {
	lifetime time.Duration
	err      error

	mu    sync.Mutex
	calls int
}

func (c *countingCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(c.lifetime)}, nil
}

func (c *countingCredential) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

var armScope = policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}}

// Clients asking for the same token at once, and later, share one fetch;
// another scope or tenant gets its own.
func TestCachedCredentialSharesTokens(t *testing.T) {
	inner := &countingCredential{lifetime: time.Hour}
	credential := cacheTokens(inner)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := credential.GetToken(t.Context(), armScope); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := credential.GetToken(t.Context(), armScope); err != nil {
		t.Fatal(err)
	}
	if inner.count() != 1 {
		t.Errorf("got %d token requests, want 1", inner.count())
	}
	other := armScope
	other.TenantID = "other"
	if _, err := credential.GetToken(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: []string{"https://vault.azure.net/.default"}}); err != nil {
		t.Fatal(err)
	}
	if inner.count() != 3 {
		t.Errorf("got %d token requests, want one per scope and tenant, 3", inner.count())
	}
}

// A token about to expire is fetched again, and a failure isn't kept.
func TestCachedCredentialRefreshes(t *testing.T) {
	inner := &countingCredential{lifetime: time.Minute}
	credential := cacheTokens(inner)
	for range 2 {
		if _, err := credential.GetToken(t.Context(), armScope); err != nil {
			t.Fatal(err)
		}
	}
	if inner.count() != 2 {
		t.Errorf("got %d token requests, want each, as the token expires within the margin", inner.count())
	}

	failing := &countingCredential{lifetime: time.Hour, err: errors.New("az: not logged in")}
	credential = cacheTokens(failing)
	for range 2 {
		if _, err := credential.GetToken(t.Context(), armScope); err == nil {
			t.Error("want the credential's error")
		}
	}
	if failing.count() != 2 {
		t.Errorf("got %d token requests, want each after a failure", failing.count())
	}
}
