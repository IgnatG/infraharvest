// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// cloudAssetService lists through Cloud Asset Inventory every type the
// catalogue maps from it.
const cloudAssetService = "cloud_asset"

// searchReadMask is what a search returns of each resource: what the
// catalogue maps it with, what its import ID is built from, and its
// labels.
const searchReadMask = "name,assetType,project,displayName,location,labels,state,additionalAttributes,parentFullResourceName"

// CloudAssetLister lists the resources of the project that Cloud Asset
// Inventory finds, of the asset types the catalogue's inventory entries
// map to Terraform types, in one search. A region's import takes those in
// the region and its zones; the import of global takes the rest: global
// resources and those in a multi-region, such as a bucket in US. Search
// needs cloudasset.assets.searchAllResources on the project, which
// roles/cloudasset.viewer grants, and the Cloud Asset API enabled in the
// project that the credentials' quota goes to.
//
// An alias of an old service (see serviceAliases) is one restricted to
// that service's types: it searches only for their asset types, and keeps
// only resources of those types.
type CloudAssetLister struct {
	harvest.ListerBase
	// types are the Terraform types to list; nil for every type the
	// catalogue maps.
	types []string
}

// List searches the project and keeps what is in the import's
// region.
func (g *CloudAssetLister) List() error {
	cat := g.catalogue()
	types := g.Types(cat)
	assetTypes := inventoryTypes(cat, types)
	if len(assetTypes) == 0 {
		return nil
	}
	ctx := g.Context()
	svc, err := cloudasset.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return err
	}
	project := g.Settings()["project"].(string)
	results, err := searchAll(ctx, svc, project, assetTypes)
	if err != nil {
		return err
	}
	region, _ := g.Settings()["region"].(compute.Region)
	resources := assetResources(results, cat, g.Provider(), project, region.Name)
	if g.types != nil {
		resources = slices.DeleteFunc(resources, func(r harvest.Resource) bool {
			return !slices.Contains(types, r.Address.Type)
		})
	}
	g.Resources = resources
	return nil
}

// Types returns the Terraform types the lister lists, sorted: those of
// the catalogue's inventory entries, or, for an alias, those of its types
// that have one.
func (g *CloudAssetLister) Types(cat *catalogue.Catalogue) []string {
	var types []string
	for _, typ := range cat.Types() {
		if e, ok := cat.Entry(typ); ok && e.Inventory != nil && (g.types == nil || slices.Contains(g.types, typ)) {
			types = append(types, typ)
		}
	}
	return types
}

// inventoryTypes returns the asset types the catalogue's inventory entries
// for types name, sorted, each once.
func inventoryTypes(cat *catalogue.Catalogue, types []string) []string {
	var assetTypes []string
	for _, typ := range types {
		if e, ok := cat.Entry(typ); ok && e.Inventory != nil && !slices.Contains(assetTypes, e.Inventory.Type) {
			assetTypes = append(assetTypes, e.Inventory.Type)
		}
	}
	slices.Sort(assetTypes)
	return assetTypes
}

// searchAll searches the project for resources of assetTypes. If the
// search rejects them, as it does when one can't be searched, it searches
// them one by one and logs those it rejects.
func searchAll(ctx context.Context, svc *cloudasset.Service, project string, assetTypes []string) ([]*cloudasset.ResourceSearchResult, error) {
	results, err := search(ctx, svc, project, assetTypes)
	if !isBadRequest(err) || len(assetTypes) == 1 {
		return results, err
	}
	log.Printf("google: cloud_asset: the search rejects the catalogue's asset types (%v); searching them one by one", err)
	results = nil
	for _, t := range assetTypes {
		found, err := search(ctx, svc, project, []string{t})
		if isBadRequest(err) {
			log.Printf("google: cloud_asset: %s can't be searched: %v", t, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		results = append(results, found...)
	}
	return results, nil
}

func search(ctx context.Context, svc *cloudasset.Service, project string, assetTypes []string) ([]*cloudasset.ResourceSearchResult, error) {
	var results []*cloudasset.ResourceSearchResult
	err := svc.V1.SearchAllResources("projects/"+project).
		AssetTypes(assetTypes...).
		PageSize(500).
		ReadMask(searchReadMask).
		Pages(ctx, func(page *cloudasset.SearchAllResourcesResponse) error {
			results = append(results, page.Results...)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("searching project %s: %w", project, err)
	}
	return results, nil
}

func isBadRequest(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest
}

// assetResources turns what a search found into the provider's resources
// in region ("" for global), through the catalogue. A resource the
// catalogue maps to no type, or to several, is logged and left out, as is
// one being deleted.
func assetResources(results []*cloudasset.ResourceSearchResult, cat *catalogue.Catalogue, provider, projectID, region string) []harvest.Resource {
	var resources []harvest.Resource
	elsewhere := 0
	for _, r := range results {
		if !inRegion(r.Location, region) {
			elsewhere++
			continue
		}
		if strings.Contains(strings.ToUpper(r.State), "DELET") {
			continue
		}
		types := cat.FromInventory(r.AssetType, r.Name)
		switch len(types) {
		case 0:
			log.Printf("%s: cloud_asset: %s (%s): no %s resource type; left out", provider, r.Name, r.AssetType, provider)
			continue
		case 1:
		default:
			log.Printf("%s: cloud_asset: %s could be %s, says the catalogue; left out", provider, r.Name, strings.Join(types, " or "))
			continue
		}
		id := assetID(r.Name, r.Project, projectID)
		name := r.DisplayName
		if name == "" {
			name = lastSegment(id)
		}
		resources = append(resources, harvest.Record(id, name, types[0], provider, assetAttributes(r, id, projectID)))
	}
	if elsewhere > 0 {
		where := "the global import"
		if region == "" {
			where = "the imports of their regions"
		}
		log.Printf("%s: cloud_asset: %d resources of project %s are in other locations, for %s", provider, elsewhere, projectID, where)
	}
	return resources
}

// regional matches the location of a resource in a region or zone, such
// as us-central1 or us-central1-a, rather than global or a multi-region
// such as us or nam4.
var regional = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+(-[a-z])?$`)

// inRegion reports whether a resource at location belongs to the import of
// region: the region and its zones, or, for global (""), what isn't in a
// region.
func inRegion(location, region string) bool {
	location = strings.ToLower(location)
	if region == "" {
		return !regional.MatchString(location)
	}
	return location == region || strings.HasPrefix(location, region+"-")
}

// assetID is a resource's full name without the service's host, which
// most of the provider's types import with: //compute.googleapis.com/
// projects/p/global/networks/n is projects/p/global/networks/n, and
// //storage.googleapis.com/b is b. A name with the project's number, which
// some services use, gets its ID instead, so that the import's state
// names the project as the provider block does.
func assetID(fullName, assetProject, projectID string) string {
	path := strings.TrimPrefix(fullName, "//")
	if _, rest, ok := strings.Cut(path, "/"); ok {
		path = rest
	} else {
		path = ""
	}
	number := strings.TrimPrefix(assetProject, "projects/")
	if prefix := "projects/" + number + "/"; number != "" && number != projectID && strings.HasPrefix(path, prefix) {
		path = "projects/" + projectID + "/" + strings.TrimPrefix(path, prefix)
	}
	return path
}

// assetAttributes records what the catalogue's import ID templates and
// skip rules use, as asset.<field>: the project's ID, the resource's
// location, its display name, the last segment of its ID (name), its
// parent's ID and the searchable attributes the search returns with it,
// such as a service account's email, in snake case. Its labels go in as
// labels.<key>, for selection rules.
func assetAttributes(r *cloudasset.ResourceSearchResult, id, projectID string) map[string]string {
	attributes := map[string]string{
		"asset.project": projectID,
		"asset.name":    lastSegment(id),
	}
	set := func(k, v string) {
		if v != "" {
			attributes["asset."+k] = v
		}
	}
	set("location", r.Location)
	set("display_name", r.DisplayName)
	if r.ParentFullResourceName != "" {
		set("parent", assetID(r.ParentFullResourceName, r.Project, projectID))
	}
	var additional map[string]interface{}
	if len(r.AdditionalAttributes) > 0 && json.Unmarshal(r.AdditionalAttributes, &additional) == nil {
		for k, v := range additional {
			if s, ok := v.(string); ok {
				set(snakeCase(k), s)
			}
		}
	}
	for k, v := range r.Labels {
		attributes["labels."+k] = v
	}
	return attributes
}

// snakeCase turns a searchable attribute's name, such as uniqueId, into
// unique_id.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// lastSegment is the name a resource's ID ends with.
func lastSegment(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// catalogue is the catalogue of the import (see SetCatalogue), or the
// built-in one.
func (g *CloudAssetLister) catalogue() *catalogue.Catalogue {
	if cat, ok := g.Settings()["catalogue"].(*catalogue.Catalogue); ok && cat != nil {
		return cat
	}
	return catalogue.MustBuiltin("google")
}

// SetCatalogue takes the catalogue of the import, which maps what
// cloud_asset lists.
func (p *Provider) SetCatalogue(c *catalogue.Catalogue) {
	p.catalogue = c
}
