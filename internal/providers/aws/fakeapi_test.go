// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// apiCall is an AWS API request as seen by a fake handler.
type apiCall struct {
	Op    string     // operation, for JSON, query and CBOR protocols (e.g. ListQueues)
	Host  string     // host the request went to (e.g. bucket.s3.us-east-1.amazonaws.com)
	Path  string     // URL path, for REST protocols (e.g. /thing-types)
	Query url.Values // URL query, for REST protocols
	Body  string
	// Protocol is the AWS protocol of the request: protocolJSON,
	// protocolQuery, protocolEC2, protocolRESTJSON, protocolRESTXML or
	// protocolCBOR (Smithy RPC v2 CBOR, whose operation ends the path).
	Protocol string
}

// The AWS protocols a request of the fake API is in (see apiCall).
const (
	protocolJSON     = "json"
	protocolQuery    = "query"
	protocolEC2      = "ec2"
	protocolRESTJSON = "rest-json"
	protocolRESTXML  = "rest-xml"
	protocolCBOR     = "rpc-v2-cbor"
)

// restXMLServices start the hosts of the REST services that answer in XML.
var restXMLServices = []string{"s3", "route53", "cloudfront"}

// fakeAPI returns a fake AWS whose answers are handle's: the body handle
// returns for each call, with the status and content type AWS answers it
// with, an error status for a body that is an error of the call's
// protocol.
func fakeAPI(handle func(apiCall) string) *providertest.Cloud {
	return providertest.New(awsAnswers(handle))
}

// awsAnswers answers each request with the body handle returns for its
// call, as fakeAPI does.
func awsAnswers(handle func(apiCall) string) providertest.Handler {
	return func(r providertest.Request) providertest.Answer {
		call := callOf(r)
		body := handle(call)
		answer := providertest.Answer{Status: http.StatusOK, ContentType: "application/json", Body: body}
		switch call.Protocol {
		case protocolJSON:
			answer.ContentType = r.Header.Get("Content-Type")
		case protocolQuery, protocolEC2:
			answer.ContentType = "text/xml"
		case protocolCBOR:
			answer.ContentType = "application/cbor"
		}
		switch {
		case strings.Contains(body, `"__type"`), call.Protocol == protocolCBOR && strings.Contains(body, "__type"):
			// AWS JSON and CBOR protocols name the error in __type and send a 4xx status.
			answer.Status = http.StatusBadRequest
		case strings.HasPrefix(body, "<Error>"):
			// REST-XML (S3) errors; S3's "not configured" errors are 404s.
			answer.Status, answer.ContentType = http.StatusNotFound, "application/xml"
		case strings.HasPrefix(body, "<ErrorResponse"), strings.HasPrefix(body, "<Response><Errors>"):
			// Query (IAM, SNS) and EC2 query errors wrap the error and send a 4xx status.
			answer.Status = http.StatusBadRequest
		case strings.HasPrefix(body, "<") && answer.ContentType == "application/json":
			answer.ContentType = "application/xml" // REST-XML (S3)
		}
		return answer
	}
}

// callOf is the call r makes.
func callOf(r providertest.Request) apiCall {
	call := apiCall{Host: r.Host, Path: r.Path, Query: r.Query, Body: r.Body, Protocol: protocolRESTJSON}
	if r.Header.Get("Smithy-Protocol") == protocolCBOR {
		_, call.Op, _ = strings.Cut(r.Path, "/operation/")
		call.Protocol = protocolCBOR
	} else if target := r.Header.Get("X-Amz-Target"); target != "" {
		call.Op, call.Protocol = target[strings.LastIndex(target, ".")+1:], protocolJSON
	} else if form, err := url.ParseQuery(r.Body); err == nil && form.Get("Action") != "" {
		call.Op, call.Protocol = form.Get("Action"), protocolQuery
		if strings.HasPrefix(r.Host, "ec2.") {
			call.Protocol = protocolEC2
		}
	} else {
		for _, service := range restXMLServices {
			if strings.HasPrefix(r.Host, service+".") || strings.Contains(r.Host, "."+service+".") {
				call.Protocol = protocolRESTXML
			}
		}
	}
	return call
}

// useFakeAPI returns a context whose listers send their requests to handle
// instead of AWS: those whose context is set to it, and the provider's own
// calls once Init is given it.
func useFakeAPI(t *testing.T, handle func(apiCall) string) context.Context {
	t.Helper()
	return fakeRun(t, fakeConfig(handle))
}

// fakeConfig is the configuration useFakeAPI fakes the API with: anonymous
// credentials, no retries, and handle answering every request.
func fakeConfig(handle func(apiCall) string) aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient:  fakeAPI(handle),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
}

// fakeRun returns a context carrying a run of its own whose services all
// use cfg, and cfg's credentials as their base credentials (see
// runConfigs.fixed), so that tests faking the API at once never see each
// other's.
func fakeRun(t *testing.T, cfg aws.Config) context.Context {
	t.Helper()
	return fakeRunIn(t.Context(), cfg)
}

// fakeRunIn is fakeRun, with a context of its own made from ctx.
func fakeRunIn(ctx context.Context, cfg aws.Config) context.Context {
	l := ratelimit.New(0)
	configsOf(l).fixed = &cfg
	return ratelimit.NewContext(ctx, l)
}

// resourceIDs returns the IDs of the resources of resourceType.
func resourceIDs(resources []harvest.Resource, resourceType string) []string {
	var ids []string
	for _, r := range resources {
		if r.Address.Type == resourceType {
			ids = append(ids, r.Observed.ID)
		}
	}
	return ids
}
