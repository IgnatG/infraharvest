// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// EndpointEnv names the base URL of a GCP emulator, such as
// http://localhost:4588 for floci-gcp. When it is set, every Google API
// call the listers make goes there, without credentials. The end-to-end
// tests use it; the Terraform provider takes its own custom endpoints.
const EndpointEnv = "INFRAHARVEST_GCP_ENDPOINT"

// clientOptions are the options of every Google REST API client: a client
// whose requests to each API go through ratelimit.Default (see
// limitedClient), or, with EndpointEnv set, one that sends each
// googleapis.com request to the emulator, keeping its path, such as
// /compute/v1/projects/.... ctx is the context of the call that needs a
// client, which the first one finds the credentials with (see
// limitedClient).
func clientOptions(ctx context.Context) []option.ClientOption {
	if target := emulator(); target != nil {
		return []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: ratelimit.Default().Transport(emulatorTransport{target: target})})}
	}
	if client := limitedClient(ctx); client != nil {
		return []option.ClientOption{option.WithHTTPClient(client)}
	}
	return nil
}

var (
	limitedOnce sync.Once
	limited     *http.Client
)

// limitedClient is a client, with the application default credentials for
// every Google Cloud API, that sends each API's requests through
// ratelimit.Default: Google's quotas are per API and project. It is nil
// if there are no credentials; each API client then looks for them itself
// and fails the same way. The first call finds the credentials with ctx.
func limitedClient(ctx context.Context) *http.Client {
	limitedOnce.Do(func() {
		// Once for the process: it only finds the credentials, and each
		// request takes its listing's context. The client outlives ctx,
		// whose listing may end first, and its token source keeps the
		// context it was made with to refresh tokens: so it keeps ctx's
		// values but not its cancellation or deadline.
		client, _, err := htransport.NewClient(context.WithoutCancel(ctx), option.WithScopes("https://www.googleapis.com/auth/cloud-platform"))
		if err != nil {
			log.Printf("google: no default credentials for a rate-limited client: %v", err)
			return
		}
		client.Transport = ratelimit.Default().Transport(client.Transport)
		limited = client
	})
	return limited
}

// emulator returns EndpointEnv as a URL, or nil.
func emulator() *url.URL {
	endpoint := os.Getenv(EndpointEnv)
	if endpoint == "" {
		return nil
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" {
		return nil
	}
	return target
}

// emulatorTransport sends requests for Google's APIs to target.
type emulatorTransport struct {
	target *url.URL
}

func (t emulatorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Hostname(), ".googleapis.com") {
		return http.DefaultTransport.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = t.target.Scheme
	out.URL.Host = t.target.Host
	out.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(out)
}
