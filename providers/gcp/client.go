// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gcp

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// EndpointEnv names the base URL of a GCP emulator, such as
// http://localhost:4588 for floci-gcp. When it is set, every Google API
// call the listers make goes there, without credentials. The end-to-end
// tests use it; the Terraform provider takes its own custom endpoints.
const EndpointEnv = "INFRAHARVEST_GCP_ENDPOINT"

// clientOptions are the options of every Google REST API client: with
// EndpointEnv set, a transport that sends each googleapis.com request to
// the emulator, keeping its path, such as /compute/v1/projects/....
func clientOptions() []option.ClientOption {
	target := emulator()
	if target == nil {
		return nil
	}
	return []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: emulatorTransport{target: target}})}
}

// grpcClientOptions are the options of every Google gRPC API client: with
// EndpointEnv set, the emulator's host, without TLS or credentials.
func grpcClientOptions() []option.ClientOption {
	target := emulator()
	if target == nil {
		return nil
	}
	return []option.ClientOption{
		option.WithEndpoint(target.Host),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
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
