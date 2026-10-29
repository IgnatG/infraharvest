// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// fakeCredential hands out a token without signing in.
type fakeCredential struct{}

func (fakeCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// settledLister is a lister of the provider, which takes its settings.
type settledLister interface {
	harvest.Lister
	harvest.SettingsTaker[serviceSettings]
}

// fakeOptions are the client options of a Resource Manager that cloud
// fakes: every request goes to it.
func fakeOptions(cloud *providertest.Cloud) *arm.ClientOptions {
	return &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: cloud}}
}

// fakeArgs are the arguments Select hands a service, for subscription
// "sub" and resourceGroup ("" for the whole subscription), with every
// request answered by cloud.
func fakeArgs(cloud *providertest.Cloud, resourceGroup string) serviceSettings {
	return serviceSettings{
		subscriptionID: "sub",
		resourceGroup:  resourceGroup,
		credential:     fakeCredential{},
		clientOptions:  fakeOptions(cloud),
	}
}

// answering answers every request with body.
func answering(body string) providertest.Handler {
	return func(providertest.Request) providertest.Answer { return providertest.JSON(body) }
}

// armRoutes answers a request for a path in bodies with its body, one for
// a path in statuses with that status and Resource Manager's error (see
// armError), and any other with 404.
func armRoutes(bodies map[string]string, statuses map[string]int) providertest.Handler {
	return func(r providertest.Request) providertest.Answer {
		if status, ok := statuses[r.Path]; ok {
			return armError(status, r.Path)
		}
		if body, ok := bodies[r.Path]; ok {
			return providertest.JSON(body)
		}
		return armError(http.StatusNotFound, r.Path)
	}
}

// armError is Resource Manager's answer of status to a request for path:
// AuthorizationFailed for 403, NotFound for 404, Failed otherwise.
func armError(status int, path string) providertest.Answer {
	code := "Failed"
	switch status {
	case http.StatusForbidden:
		code = "AuthorizationFailed"
	case http.StatusNotFound:
		code = "NotFound"
	}
	return providertest.Status(status, `{"error":{"code":"`+code+`","message":"answered `+http.StatusText(status)+` for `+path+`"}}`)
}

// routed is a cloud that answers as armRoutes does, with bodies and no
// statuses.
func routed(bodies map[string]string) *providertest.Cloud {
	return providertest.New(armRoutes(bodies, nil))
}
