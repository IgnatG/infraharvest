// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"
	"github.com/magodo/armid"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// apiVersionAttribute is the attribute an azapi_resource records its API
// version in, which the azapi catalogue's import ID template appends to
// the resource's ID as ?api-version= (see withAPIVersions).
const apiVersionAttribute = "api_version"

// stableAPIVersion is an API version that is generally available: a
// date alone, without -preview, -beta or the like.
var stableAPIVersion = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// newestStable returns the newest of versions that is stable, or "" if
// none is. API versions are dates, which sort as strings.
func newestStable(versions []string) string {
	newest := ""
	for _, v := range versions {
		if stableAPIVersion.MatchString(v) && v > newest {
			newest = v
		}
	}
	return newest
}

// namespaceVersions are the API versions of a resource provider
// namespace's resource types, by type without the namespace, in lower
// case (such as fleets or networkmanagers/verifierworkspaces), or why
// they couldn't be read. once makes one request of them, however many
// listers ask at once.
type namespaceVersions struct {
	once  sync.Once
	types map[string][]string
	err   error
}

// versionCache holds what Resource Manager's providers API said of each
// namespace, by Resource Manager endpoint and namespace in lower case:
// a namespace's types and their API versions are the same in every
// subscription of a cloud, so each is asked for once a run. A request that
// failed isn't kept: the next lister to ask asks again.
type versionCache struct {
	namespaces sync.Map
}

// apiVersions is the run's cache.
var apiVersions = &versionCache{}

// of returns the API versions of namespace's types, reading them with one
// GET /subscriptions/{subscriptionID}/providers/{namespace} the first
// time, through options, whose transport is the subscription's limiter.
func (c *versionCache) of(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, subscriptionID, namespace string) (map[string][]string, error) {
	endpoint := cloud.AzurePublic.Services[cloud.ResourceManager].Endpoint
	if options != nil {
		if s, ok := options.Cloud.Services[cloud.ResourceManager]; ok && s.Endpoint != "" {
			endpoint = s.Endpoint
		}
	}
	entry, _ := c.namespaces.LoadOrStore(strings.ToLower(endpoint+"|"+namespace), &namespaceVersions{})
	n := entry.(*namespaceVersions)
	n.once.Do(func() {
		client, err := armresources.NewProvidersClient(subscriptionID, credential, options)
		if err != nil {
			n.err = err
			return
		}
		resp, err := client.Get(ctx, namespace, nil)
		if err != nil {
			n.err = err
			return
		}
		n.types = map[string][]string{}
		for _, t := range resp.ResourceTypes {
			if t == nil || t.ResourceType == nil {
				continue
			}
			var versions []string
			for _, v := range t.APIVersions {
				if v != nil {
					versions = append(versions, *v)
				}
			}
			n.types[strings.ToLower(*t.ResourceType)] = versions
		}
	})
	if n.err != nil {
		// A failure isn't what the namespace is: a subscription the
		// credential may not read, a throttled or an interrupted request.
		// The next lister to ask, as of another subscription or a later run
		// of the same process (the MCP server), asks again.
		c.namespaces.CompareAndDelete(strings.ToLower(endpoint+"|"+namespace), n)
	}
	return n.types, n.err
}

// withAPIVersions records, of each azapi_resource of resources, the
// newest stable API version of its type, which its import ID and its
// configuration's type then pin, rather than the newest azapi's schemas
// have, which may be a preview. It asks Resource Manager once per
// namespace of those resources, and not at all without one. A type
// without a stable version, or whose namespace can't be read, is left to
// azapi's choice, and logged once.
func withAPIVersions(ctx context.Context, cache *versionCache, credential azcore.TokenCredential, options *arm.ClientOptions, subscriptionID string, resources []harvest.Resource) {
	logged := map[string]bool{}
	logOnce := func(key, format string, args ...any) {
		if !logged[key] {
			logged[key] = true
			log.Printf(format, args...)
		}
	}
	for _, r := range resources {
		if r.Address == nil || r.Address.Type != azapiType || r.Observed == nil || r.Observed.Attributes == nil {
			continue
		}
		rid, err := armid.ParseResourceId(r.Observed.ID)
		if err != nil {
			continue
		}
		namespace, typ, ok := strings.Cut(rid.TypeString(), "/")
		if !ok {
			continue
		}
		types, err := cache.of(ctx, credential, options, subscriptionID, namespace)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logOnce(strings.ToLower(namespace), "azurerm: resource_graph: the API versions of %s: %v; azapi picks the newest its schemas have for its resources", namespace, err)
			continue
		}
		versions := types[strings.ToLower(typ)]
		if v := newestStable(versions); v != "" {
			r.Observed.Attributes[apiVersionAttribute] = v
			continue
		}
		reason := "only preview API versions"
		if len(versions) == 0 {
			reason = "no API versions Resource Manager lists"
		}
		logOnce(strings.ToLower(rid.TypeString()), "azurerm: resource_graph: %s has %s; azapi picks the newest its schemas have", rid.TypeString(), reason)
	}
}
