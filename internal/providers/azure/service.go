// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// serviceSettings are the settings Provider.Lister passes each service
// (see harvest.ListerOf).
type serviceSettings struct {
	// subscriptionID is the subscription to list.
	subscriptionID string
	// resourceGroup is --resource-group, "" for the whole subscription.
	resourceGroup string
	// credential signs every request in.
	credential azcore.TokenCredential
	// clientOptions are the options of every Resource Manager client.
	clientOptions *arm.ClientOptions
	// catalogue is the import's catalogue, nil for the built-in one.
	catalogue *catalogue.Catalogue
	// listed is what the import's other services listed, for the services
	// that list from it (see Provider.Profile).
	listed []harvest.Resource
	// azapi is --azapi: resource_graph imports what azurerm has no type
	// for as azapi_resource (see azapiCandidates).
	azapi bool
}

// Service is what every Azure lister embeds: the service state, and the
// settings Provider.Lister passes it.
type Service struct {
	harvest.ListerOf[serviceSettings]
}

// clientSettings returns what the service's Resource Manager clients need:
// the subscription, --resource-group ("" for the whole subscription), the
// credential and the client options.
func (az *Service) clientSettings() (subscriptionID, resourceGroup string, credential azcore.TokenCredential, options *arm.ClientOptions) {
	s := az.Settings()
	return s.subscriptionID, s.resourceGroup, s.credential, s.clientOptions
}

// customCloud reports whether the service's clients reach a cloud other
// than Azure's own (public, US Government and China), such as Azure Stack
// or an emulator that ARM_ENVIRONMENT=stack describes.
func (az *Service) customCloud() bool {
	_, _, _, options := az.clientSettings()
	if options == nil {
		return false
	}
	endpoint := options.Cloud.Services[cloud.ResourceManager].Endpoint
	if endpoint == "" {
		return false
	}
	for _, own := range []cloud.Configuration{cloud.AzurePublic, cloud.AzureGovernment, cloud.AzureChina} {
		if own.Services[cloud.ResourceManager].Endpoint == endpoint {
			return false
		}
	}
	return true
}

// listedInFull reports whether a parent's list response carried all of its
// children: a field the response left out (nil) never did, and in a custom
// cloud nor did an empty one, as some emulators leave out children created
// on their own.
func listedInFull[T any](children []*T, custom bool) bool {
	return len(children) > 0 || children != nil && !custom
}

// recordBare adds a resource of resourceType, imported by id and
// labelled name, with no attributes.
func (az *Service) recordBare(id, name, resourceType string) {
	az.Resources = append(az.Resources, harvest.RecordBare(id, name, resourceType, az.Provider()))
}

// recordTagged adds a resource of resourceType, imported by id and
// labelled name, with the tags its list response carried as its
// attributes (see armTagAttributes), an empty map of its own without
// tags, as recordBare's.
func (az *Service) recordTagged(id, name, resourceType string, tags map[string]*string) {
	az.Resources = append(az.Resources, harvest.Record(id, name, resourceType, az.Provider(), armTagAttributes(tags)))
}

// listAll returns the non-nil items of every page of pager, in order, as
// items picks them out of each page. On the first page that fails it stops
// and returns the items of the pages before it with the error, so that a
// lister that would rather keep part of a listing than none can record
// them and return the error as partial (see harvest.Partial).
func listAll[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T) ([]*T, error) {
	var collected []*T
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return collected, err
		}
		for _, item := range items(page) {
			if item != nil {
				collected = append(collected, item)
			}
		}
	}
	return collected, nil
}
