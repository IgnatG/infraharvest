// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// fakeCredential hands out a token without signing in.
type fakeCredential struct{}

func (fakeCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// fakeTransport answers every request with body, and records the path and
// the body of each request in order. Requests may come at once.
type fakeTransport struct {
	mu    sync.Mutex
	body  string
	paths []string
	sent  []string
}

func (f *fakeTransport) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	var sent []byte
	if r.Body != nil {
		var err error
		if sent, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
	}
	f.sent = append(f.sent, string(sent))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    r,
	}, nil
}

// fakeArgs are the arguments Select hands a service, for subscription
// "sub" and resourceGroup ("" for the whole subscription), with every
// request answered by transport.
func fakeArgs(transport *fakeTransport, resourceGroup string) map[string]interface{} {
	return map[string]interface{}{
		"subscription_id": "sub",
		"resource_group":  resourceGroup,
		"credential":      azcore.TokenCredential(fakeCredential{}),
		"client_options":  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	}
}
