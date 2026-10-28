// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"fmt"
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

// EndpointEnv names the base URL of a Google Cloud emulator, such as
// http://localhost:4588 for floci-gcp. When it is set, every Google API
// call the listers make goes there, without credentials. The end-to-end
// tests use it; the Terraform provider takes its own custom endpoints.
const EndpointEnv = "INFRAHARVEST_GOOGLE_ENDPOINT"

// clientOptions are the options of every Google REST API client: a client
// whose requests to each API go through the limiter of ctx's run (see
// ratelimit.FromContext) and are sent again when throttled or failed for
// the time being (see limitedClient and retryTransport), or, with
// EndpointEnv set, one that sends each googleapis.com request to the
// emulator, keeping its path, such as /compute/v1/projects/.... ctx is the
// context of the call that needs a client, which the first one finds the
// credentials with (see credentialsClient).
func clientOptions(ctx context.Context) []option.ClientOption {
	if target := emulator(); target != nil {
		run := ratelimit.FromContext(ctx)
		return []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: retryTransport{base: run.Transport(emulatorTransport{target: target}), delay: runRetryDelay(run)}})}
	}
	if client := limitedClient(ctx); client != nil {
		return []option.ClientOption{option.WithHTTPClient(client)}
	}
	return nil
}

// limitedClientKey is the key of a run's limitedClient kept with its
// limiter.
type limitedClientKey struct{}

// limitedClient is a client, with the credentials of credentialsClient,
// that sends each API's requests through the limiter of ctx's run, and
// sends again those Google throttles or fails for the time being (see
// retryTransport). Every project of the run shares it, kept with the
// run's limiter: Google charges most quotas to the credentials' quota
// project, not to the project a request reads, so a limit per project
// would only multiply the rate against the same quota. It is nil if there
// are no credentials; each API client then looks for them itself and
// fails the same way.
func limitedClient(ctx context.Context) *http.Client {
	return limitedBy(ratelimit.FromContext(ctx), credentialsClient(ctx))
}

// limitedBy returns base limited by run, made the first time the run asks
// (see limitedClient), or nil for no base.
func limitedBy(run *ratelimit.Limiter, base *http.Client) *http.Client {
	if base == nil {
		return nil
	}
	// Kept doesn't let a value it makes ask the run for another.
	delay := runRetryDelay(run)
	return ratelimit.Kept(run, limitedClientKey{}, func() *http.Client {
		client := *base
		cloudAsset := ratelimit.New(min(cloudAssetRate, run.Rate()))
		client.Transport = retryTransport{base: byHost{
			cloudAsset: cloudAsset.Transport(base.Transport),
			other:      run.Transport(base.Transport),
		}, delay: delay}
		return &client
	})
}

// cloudAssetRate is how many Cloud Asset requests a second a run sends at
// most: Cloud Asset Inventory allows 400 searches a minute per consumer
// project, the credentials' quota project, which every project of a run
// shares, so the run's rate per host, 20 a second, would exceed it as
// soon as projects are searched in parallel.
const cloudAssetRate = 6.0

// cloudAssetHost is the host of Cloud Asset Inventory's API.
const cloudAssetHost = "cloudasset.googleapis.com"

// byHost sends Cloud Asset requests through cloudAsset, paced at
// cloudAssetRate, or the run's rate if that is lower, and every other
// request through other, paced at the run's rate.
type byHost struct {
	cloudAsset, other http.RoundTripper
}

func (b byHost) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.EqualFold(req.URL.Hostname(), cloudAssetHost) {
		return b.cloudAsset.RoundTrip(req)
	}
	return b.other.RoundTrip(req)
}

var (
	credentialsOnce sync.Once
	credentials     *http.Client
)

// credentialsClient is a client, with the credentials the Terraform
// provider would use (see credentialOptions) for every Google Cloud API,
// that limits nothing: each run limits it with its own limiter (see
// limitedClient). It is nil if there are no credentials. The first call
// finds the credentials with ctx.
func credentialsClient(ctx context.Context) *http.Client {
	credentialsOnce.Do(func() {
		// Once for the process: it only finds the credentials, and each
		// request takes its listing's context. The client outlives ctx,
		// whose listing may end first, and its token source keeps the
		// context it was made with to refresh tokens: so it keeps ctx's
		// values but not its cancellation or deadline, nor its run's
		// limiter, which it would keep for the process, with all the run
		// keeps with it.
		ctx := ratelimit.NewContext(context.WithoutCancel(ctx), ratelimit.Default())
		opts, err := credentialOptions(ctx)
		if err != nil {
			log.Printf("google: the credentials the environment names can't be used: %v", err)
			opts = []option.ClientOption{option.WithTokenSource(failingTokenSource{err: fmt.Errorf("google: %w", err)})}
		}
		client, _, err := htransport.NewClient(ctx, opts...)
		if err != nil {
			log.Printf("google: no default credentials for a rate-limited client: %v", err)
			return
		}
		credentials = client
	})
	return credentials
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
