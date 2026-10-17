// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// tokenRefreshMargin is how long before a cached token expires it is
// fetched again, so that no request goes out with a token about to end.
const tokenRefreshMargin = 5 * time.Minute

// cachedCredential keeps the tokens credential returns, by what they were
// asked for, until shortly before each expires (see tokenRefreshMargin).
// azcore caches a token only in each client's bearer token policy, and
// resource_graph's lookups build new clients all the time: without it,
// each would start an `az account get-access-token`, which the Azure CLI
// credential runs one at a time, or ask the managed identity endpoint
// again. Concurrent callers for the same token wait for one fetch.
type cachedCredential struct {
	credential azcore.TokenCredential

	mu     sync.Mutex
	tokens map[string]*cachedToken
}

// cachedToken is one token cachedCredential keeps; mu is held while it
// is fetched.
type cachedToken struct {
	mu    sync.Mutex
	token azcore.AccessToken
	ok    bool
}

// cacheTokens returns credential with its tokens kept (see
// cachedCredential).
func cacheTokens(credential azcore.TokenCredential) *cachedCredential {
	return &cachedCredential{credential: credential, tokens: map[string]*cachedToken{}}
}

// GetToken returns the token kept for options, or fetches it when there
// is none or it expires within tokenRefreshMargin.
func (c *cachedCredential) GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	key := strings.Join(options.Scopes, " ") + "\x00" + options.TenantID + "\x00" + options.Claims + "\x00" + strconv.FormatBool(options.EnableCAE)
	c.mu.Lock()
	t, ok := c.tokens[key]
	if !ok {
		t = &cachedToken{}
		c.tokens[key] = t
	}
	c.mu.Unlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ok && time.Until(t.token.ExpiresOn) > tokenRefreshMargin {
		return t.token, nil
	}
	token, err := c.credential.GetToken(ctx, options)
	if err != nil {
		return token, err
	}
	t.token, t.ok = token, true
	return token, nil
}
