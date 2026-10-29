// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// fakeGoogle stands up a fake Google Cloud answering with handle, as the
// endpoint EndpointEnv names, until t ends, and returns it: every API
// client of a run sends its requests there. As it sets the environment,
// a test that uses it can't run in parallel.
func fakeGoogle(t *testing.T, handle providertest.Handler) *providertest.Cloud {
	t.Helper()
	cloud := providertest.New(handle)
	t.Setenv(EndpointEnv, cloud.Serve(t))
	return cloud
}

// jsonOf is a 200 answer with v as its JSON body.
func jsonOf(v any) providertest.Answer {
	body, err := json.Marshal(v)
	if err != nil {
		return providertest.Status(http.StatusInternalServerError, `{"error":{"code":500,"message":"`+err.Error()+`"}}`)
	}
	return providertest.JSON(string(body))
}

// unexpected answers a request a test doesn't expect with a 404 that
// names its path.
func unexpected(r providertest.Request) providertest.Answer {
	return providertest.Status(http.StatusNotFound, `{"error":{"code":404,"message":"unexpected `+r.Method+` `+r.Path+`"}}`)
}
