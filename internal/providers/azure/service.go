// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"log"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The keys of the settings Provider.Select passes each service, written
// only by serviceSettings.args and read only by settingsOf, so that the
// two can't drift apart.
const (
	subscriptionIDKey = "subscription_id"
	resourceGroupKey  = "resource_group"
	credentialKey     = "credential"
	clientOptionsKey  = "client_options"
	catalogueKey      = "catalogue"
	listedKey         = "listed"
)

// serviceSettings are the settings Provider.Select passes each service,
// typed: the service gets them as a map (see harvest.ListerConfig), which
// args makes and settingsOf reads back.
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
	// that list from it (see Provider.DependentServices).
	listed []harvest.Resource
}

// args returns the settings as a service's settings map.
func (s serviceSettings) args() map[string]interface{} {
	return map[string]interface{}{
		subscriptionIDKey: s.subscriptionID,
		credentialKey:     s.credential,
		clientOptionsKey:  s.clientOptions,
		resourceGroupKey:  s.resourceGroup,
		catalogueKey:      s.catalogue,
		listedKey:         s.listed,
	}
}

// settingsOf returns the settings in a service's settings map, as args
// makes it. A setting that is missing, or of another type, comes back as
// its zero value.
func settingsOf(settings map[string]interface{}) serviceSettings {
	var s serviceSettings
	s.subscriptionID, _ = settings[subscriptionIDKey].(string)
	s.resourceGroup, _ = settings[resourceGroupKey].(string)
	s.credential, _ = settings[credentialKey].(azcore.TokenCredential)
	s.clientOptions, _ = settings[clientOptionsKey].(*arm.ClientOptions)
	s.catalogue, _ = settings[catalogueKey].(*catalogue.Catalogue)
	s.listed, _ = settings[listedKey].([]harvest.Resource)
	return s
}

// Service is what every Azure lister embeds: the service
// state, and the settings Provider.Select passes it as
// arguments.
type Service struct {
	harvest.ListerBase
}

// clientSettings returns what the service's Resource Manager clients need:
// the subscription, --resource-group ("" for the whole subscription), the
// credential and the client options. An argument that is missing, or of
// another type, comes back as its zero value.
func (az *Service) clientSettings() (subscriptionID, resourceGroup string, credential azcore.TokenCredential, options *arm.ClientOptions) {
	s := settingsOf(az.Settings())
	return s.subscriptionID, s.resourceGroup, s.credential, s.clientOptions
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
// and returns the items of the pages before it with the error.
func listAll[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T) ([]*T, error) {
	return collectPages(ctx, pager, items, false)
}

// listAllLenient is listAll for listings where part of the result beats
// none: a page after the first that fails is logged, and the items of the
// pages before it are returned without an error. A first page that fails
// is still an error.
func listAllLenient[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T) ([]*T, error) {
	return collectPages(ctx, pager, items, true)
}

// collectPages walks pager for listAll and, with lenient, listAllLenient.
func collectPages[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T, lenient bool) ([]*T, error) {
	var collected []*T
	for first := true; pager.More(); first = false {
		page, err := pager.NextPage(ctx)
		if err != nil {
			if lenient && !first {
				log.Printf("azurerm: a page of a list failed, keeping the %d items listed before it: %v", len(collected), err)
				return collected, nil
			}
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
