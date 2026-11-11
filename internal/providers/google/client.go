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
	"github.com/IgnatG/infraharvest/internal/runstate"
)

// EndpointEnv names the base URL of a Google Cloud emulator, such as
// http://localhost:4588 for floci-gcp. When it is set, every Google API
// call the listers make goes there, without credentials. The end-to-end
// tests use it; the Terraform provider takes its own custom endpoints.
const EndpointEnv = "INFRAHARVEST_GOOGLE_ENDPOINT"

// clientOptions are the options of every Google REST API client: a client
// whose requests to each API go through the limiter of ctx's run (see
// runstate.FromContext) and are sent again when throttled or failed for
// the time being (see limitedClient and retryTransport), or, with
// EndpointEnv set, one that sends each googleapis.com request to the
// emulator, keeping its path, such as /compute/v1/projects/.... ctx is the
// context of the call that needs a client, which the first one finds the
// credentials with (see credentialsClient).
//
// project is the project the client reads, whose impersonated service
// account, if an environment names one (see runCaches.setImpersonation), the
// client's credentials are of; "" for none.
func clientOptions(ctx context.Context, project string) []option.ClientOption {
	target, err := emulator()
	if err != nil {
		// A call to Google itself would read a real project where the
		// emulator was meant: every call fails instead, saying why.
		return []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: failingTransport{err: err}})}
	}
	if target != nil {
		run := runstate.FromContext(ctx)
		return []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: retryTransport{base: run.Limiter().Transport(emulatorTransport{target: target}), delay: runRetryDelay(run)}})}
	}
	if client := limitedClient(ctx, impersonation(ctx, project)); client != nil {
		return []option.ClientOption{option.WithHTTPClient(client)}
	}
	return nil
}

// limitedClientKey is the key of a run's limitedClient kept with the
// run: each service account impersonated has a client of its own.
type limitedClientKey struct{ impersonate string }

// limitedClient is a client, with the credentials of credentialsClient,
// that sends each API's requests through the limiter of ctx's run, and
// sends again those Google throttles or fails for the time being (see
// retryTransport). Every project of the run shares it, kept with the
// run: Google charges most quotas to the credentials' quota
// project, not to the project a request reads, so a limit per project
// would only multiply the rate against the same quota. It is nil if there
// are no credentials; each API client then looks for them itself and
// fails the same way.
func limitedClient(ctx context.Context, impersonate string) *http.Client {
	return limitedByKey(runstate.FromContext(ctx), impersonate, credentialsClient(ctx, impersonate))
}

// limitedByKey returns base limited by run, made the first time the run
// asks (see limitedClient), for the credentials of the service account
// impersonate, "" for none; nil for no base.
func limitedByKey(run *runstate.Run, impersonate string, base *http.Client) *http.Client {
	if base == nil {
		return nil
	}
	// Kept doesn't let a value it makes ask the run for another.
	delay := runRetryDelay(run)
	return runstate.Kept(run, limitedClientKey{impersonate}, func() *http.Client {
		client := *base
		cloudAsset := ratelimit.New(min(cloudAssetRate, run.Limiter().Rate()))
		client.Transport = retryTransport{base: byHost{
			cloudAsset: cloudAsset.Transport(base.Transport),
			other:      run.Limiter().Transport(base.Transport),
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

// credentialed is the client of one service account's credentials, found
// once.
type credentialed struct {
	once   sync.Once
	client *http.Client
}

// credentialsKey is the key of a run's credentials kept with the run.
type credentialsKey struct{}

// credentialsOf returns the clients of the credentials ctx's run found, by
// the service account they impersonate ("" for none). They are kept with
// the run (see runstate.Kept), so that another run in the process looks
// for them again: credentials that couldn't be found or used, such as
// before `gcloud auth application-default login`, aren't kept failing for
// the life of the process.
func credentialsOf(ctx context.Context) *sync.Map {
	return runstate.Kept(runstate.FromContext(ctx), credentialsKey{}, func() *sync.Map { return &sync.Map{} })
}

// credentialsClient is a client, with the credentials the Terraform
// provider would use (see credentialOptions) for every Google Cloud API,
// that limits nothing: each run limits it with its own limiter (see
// limitedClient). It is nil if there are no credentials. The first call
// of ctx's run finds the credentials with ctx. impersonate is the service
// account to impersonate with them, "" for
// GOOGLE_IMPERSONATE_SERVICE_ACCOUNT's or none.
func credentialsClient(ctx context.Context, impersonate string) *http.Client {
	kept, _ := credentialsOf(ctx).LoadOrStore(impersonate, &credentialed{})
	found := kept.(*credentialed)
	found.once.Do(func() {
		// Once for the run: it only finds the credentials, and each
		// request takes its listing's context. The client outlives ctx,
		// whose listing may end first, and its token source keeps the
		// context it was made with to refresh tokens: so it keeps ctx's
		// values but not its cancellation or deadline, nor its run, whose
		// limiter it would otherwise wait on to refresh a token.
		ctx := runstate.NewContext(context.WithoutCancel(ctx), runstate.Default())
		opts, err := credentialOptions(ctx, impersonate)
		if err != nil {
			log.Printf("google: the credentials the environment names can't be used: %v", err)
			opts = []option.ClientOption{option.WithTokenSource(failingTokenSource{err: fmt.Errorf("google: %w", err)})}
		}
		client, _, err := htransport.NewClient(ctx, opts...)
		if err != nil {
			log.Printf("google: no default credentials for a rate-limited client: %v", err)
			return
		}
		found.client = client
	})
	return found.client
}

// emulator returns EndpointEnv as a URL, nil if it is unset, or an error
// if it is set to something that isn't a URL with a scheme and a host,
// such as localhost:4588.
func emulator() (*url.URL, error) {
	endpoint := os.Getenv(EndpointEnv)
	if endpoint == "" {
		return nil, nil
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("google: %s=%q isn't a URL such as http://localhost:4588", EndpointEnv, endpoint)
	}
	return target, nil
}

// failingTransport fails every request with err.
type failingTransport struct {
	err error
}

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
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
