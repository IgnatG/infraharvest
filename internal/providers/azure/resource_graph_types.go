// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// lookupParallel is how many resources resource_graph maps through aztft
// at once. The subscription's limiter (see subscriptionLimiter) paces the
// requests that still reach Resource Manager, well below its read limits;
// this bounds how many lookups wait on it, and the goroutines they hold.
const lookupParallel = 8

// mapping is what one listed resource maps to: its azurerm types, each
// with the Azure ID it stands for and the ID Terraform imports it by; or,
// when leftOut isn't empty, why it maps to none.
type mapping struct {
	types []aztft.Type
	ids   []string
	// leftOut follows the resource's ID in the line that logs it left
	// out, such as ": no azurerm resource type; left out".
	leftOut string
}

// records turns m into the resources it records, with attributes, or logs
// why id is left out.
func (m mapping) records(id string, attributes map[string]string) []harvest.Resource {
	if m.leftOut != "" {
		log.Printf("azurerm: resource_graph: %s%s", id, m.leftOut)
		return nil
	}
	resources := make([]harvest.Resource, 0, len(m.types))
	for i, t := range m.types {
		resources = append(resources, harvest.Record(m.ids[i], lastSegment(t.AzureId.String()), t.TFType, "azurerm", attributes))
	}
	return resources
}

// fromQuery maps id through query, which aztft answers.
func fromQuery(id string, query queryType) mapping {
	types, ids, exact, err := query(id)
	switch {
	case err != nil:
		return mapping{leftOut: ": " + err.Error()}
	case len(types) == 0:
		return mapping{leftOut: ": no azurerm resource type; left out"}
	case !exact:
		names := make([]string, len(types))
		for i, t := range types {
			names[i] = t.TFType
		}
		return mapping{leftOut: " could be " + strings.Join(names, " or ") + "; left out"}
	}
	return mapping{types: types, ids: ids}
}

// The azurerm types of a Log Analytics workspace's tables, which the
// table's type in its listing decides between.
const (
	microsoftTableType = "azurerm_log_analytics_workspace_table_microsoft"
	customLogTableType = "azurerm_log_analytics_workspace_table_custom_log"
)

// fromListing maps a resource from what the listing returned with it,
// with no request, where aztft would otherwise ask Resource Manager once
// per resource: it returns false for any other resource.
//
// That is a Log Analytics workspace's table, of which every workspace has
// hundreds: its type, which builtInAttributes recorded in attributes as
// table_type, decides between the Microsoft and the custom log table
// types, as aztft decides from a GET of each. Tables of restored logs and
// of a search job's results, which azurerm has no type for, are left out.
// A table listed without its type is left to aztft.
func fromListing(res azlist.AzureResource, attributes map[string]string) (mapping, bool) {
	if !strings.EqualFold(res.Id.TypeString(), workspaceTableType) {
		return mapping{}, false
	}
	var typ string
	switch tableType := attributes["table_type"]; {
	case tableType == "":
		return mapping{}, false
	case strings.EqualFold(tableType, "Microsoft"):
		typ = microsoftTableType
	case strings.EqualFold(tableType, "CustomLog"):
		typ = customLogTableType
	case strings.EqualFold(tableType, "RestoredLogs"):
		return mapping{leftOut: ": a table of restored logs, which azurerm has no resource type for; left out"}, true
	case strings.EqualFold(tableType, "SearchResults"):
		return mapping{leftOut: ": a table of a search job's results, which azurerm has no resource type for; left out"}, true
	default:
		return mapping{leftOut: fmt.Sprintf(": a table of type %q, which azurerm has no resource type for; left out", tableType)}, true
	}
	// A table's import ID needs no request: aztft builds it from the ID.
	id := res.Id.String()
	importID, err := aztft.QueryId(id, typ, nil)
	if err != nil {
		return mapping{leftOut: ": " + err.Error()}, true
	}
	return mapping{types: []aztft.Type{{AzureId: res.Id, TFType: typ}}, ids: []string{importID}}, true
}

// newLookup returns the queryType resource_graph maps resources with that
// neither the catalogue nor fromListing maps, and the count of the
// requests it sends to Resource Manager.
//
// aztft asks Resource Manager about a resource when its ID could be
// several azurerm types, such as a function app and a web app, to choose
// between them, and when the type has parts azurerm imports as resources
// of their own, such as a network interface's security group association,
// to find them: both with a GET of the resource, which the listing
// already returned. Those GETs are answered with what the listing returned
// (see listingTransport), so aztft reads the resource from the listing
// instead. A resource whose mapping fails on what the listing returned,
// which may leave out something aztft reads, is asked about again, of
// Resource Manager itself. Requests for anything else, such as a storage
// queue's import ID, go to Resource Manager.
//
// Every request fails once ctx is done: aztft sends its own without a
// context, so an interrupt would otherwise not stop them.
func newLookup(ctx context.Context, credential azcore.TokenCredential, options arm.ClientOptions, listed []azlist.AzureResource) (queryType, *atomic.Int64) {
	next := options.Transport
	if next == nil {
		next = http.DefaultClient
	}
	bodies := map[string]map[string]interface{}{}
	for _, res := range listed {
		if _, ok := res.Properties["id"]; ok {
			bodies[strings.ToUpper(res.Id.String())] = res.Properties
		}
	}
	sent := &atomic.Int64{}
	apiOption := func(answer map[string]map[string]interface{}) (*aztft.APIOption, *listingTransport) {
		transport := &listingTransport{ctx: ctx, listed: answer, next: next, sent: sent}
		clientOptions := options
		clientOptions.Transport = transport
		return &aztft.APIOption{Cred: credential, ClientOption: clientOptions}, transport
	}
	return func(id string) ([]aztft.Type, []string, bool, error) {
		fromListed, transport := apiOption(bodies)
		types, ids, exact, err := aztft.QueryTypeAndId(id, fromListed)
		if err == nil || transport.answered.Load() == 0 {
			return types, ids, exact, err
		}
		direct, _ := apiOption(nil)
		return aztft.QueryTypeAndId(id, direct)
	}, sent
}

// listingTransport sends requests on to next, except a GET of a resource
// the listing returned, with no query parameter but the API version, which
// it answers with the resource as listed: Resource Graph returns each
// resource as its GET does, and azlist lists child resources with
// Resource Manager's list APIs, which return the same.
type listingTransport struct {
	ctx context.Context
	// listed are the listed resources, by upper-case ID.
	listed map[string]map[string]interface{}
	next   policy.Transporter
	// sent counts the requests sent on to next, for every transport of a
	// listing; answered, those this transport answered.
	sent     *atomic.Int64
	answered atomic.Int64
}

func (t *listingTransport) Do(r *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	if body, ok := t.listedBody(r); ok {
		t.answered.Add(1)
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       r,
		}, nil
	}
	t.sent.Add(1)
	return t.next.Do(r)
}

// listedBody returns the listed resource r gets, as JSON, if it is one.
func (t *listingTransport) listedBody(r *http.Request) ([]byte, bool) {
	if len(t.listed) == 0 || r.Method != http.MethodGet {
		return nil, false
	}
	for key := range r.URL.Query() {
		if !strings.EqualFold(key, "api-version") {
			return nil, false
		}
	}
	properties, ok := t.listed[strings.ToUpper(strings.TrimSuffix(r.URL.Path, "/"))]
	if !ok {
		return nil, false
	}
	body, err := json.Marshal(properties)
	if err != nil {
		return nil, false
	}
	return body, true
}

// inParallel calls do with each of items, up to lookupParallel at once,
// and returns when every call has.
func inParallel(items []int, do func(int)) {
	slots := make(chan struct{}, lookupParallel)
	var wg sync.WaitGroup
	for _, item := range items {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			do(item)
		})
	}
	wg.Wait()
}
