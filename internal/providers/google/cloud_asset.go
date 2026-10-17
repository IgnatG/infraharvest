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
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// cloudAssetService lists through Cloud Asset Inventory every type the
// catalogue maps from it.
const cloudAssetService = "cloud_asset"

// searchReadMask is what a search returns of each resource: what the
// catalogue maps it with, what its import ID is built from, and its
// labels and description, which tell what created some of it.
const searchReadMask = "name,assetType,project,displayName,description,location,labels,state,additionalAttributes,parentFullResourceName"

// CloudAssetLister lists the resources of the project that Cloud Asset
// Inventory finds, of the asset types the catalogue's inventory entries
// map to Terraform types, in one search. A region's import takes those in
// the region and its zones; the import of global takes the rest: global
// resources, those in a multi-region, such as a bucket in US, and the
// subnetworks of every region, with their networks (see rootLocation).
// Search
// needs cloudasset.assets.searchAllResources on the project, which
// roles/cloudasset.viewer grants, and the Cloud Asset API enabled in the
// project that the credentials' quota goes to.
type CloudAssetLister struct {
	harvest.ListerBase
}

// List searches the project, or takes the search an import of another of
// its regions made (see searchOnce), and keeps what is in the import's
// region, with the disks that are instances' boot disks, the instances
// managed instance groups created and the node pools of Autopilot
// clusters marked (see markInstances and markAutopilotPools).
// When the instances or the clusters can't be listed to mark them, the
// listing fails in part (see harvest.Partial): it keeps every resource,
// and the disks and node pools it couldn't tell about are left out by
// default.
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
	project, err := projectSetting(g.Settings())
	if err != nil {
		return err
	}
	results, err := searchOnce(ctx, svc, project, assetTypes)
	if err != nil {
		return err
	}
	region := regionSetting(g.Settings())
	resources := assetResources(results, cat, g.Provider(), project, region.Name)
	var unmarked []error
	for _, mark := range []func(context.Context, string, []*cloudasset.ResourceSearchResult, []harvest.Resource) error{markInstances, markAutopilotPools} {
		if err := mark(ctx, project, results, resources); err != nil {
			if ctx.Err() != nil {
				return err
			}
			unmarked = append(unmarked, err)
		}
	}
	g.Resources = resources
	return harvest.Partial(errors.Join(unmarked...))
}

// Types returns the Terraform types the lister lists, sorted: those of
// the catalogue's inventory entries.
func (*CloudAssetLister) Types(cat *catalogue.Catalogue) []string {
	var types []string
	for _, typ := range cat.Types() {
		if e, ok := cat.Entry(typ); ok && e.Inventory != nil {
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

// searches are the searches the process has made, by searchKey: one
// command imports a project region by region, each import taking from the
// same project-wide search what is in its region, so the search runs once
// for all of them rather than once for each.
var searches projectLists[[]*cloudasset.ResourceSearchResult]

// searchOnce returns what searchAll finds in the project for assetTypes,
// which are sorted, searching only the first time the process asks (see
// projectLists): the imports of a project's regions and of global share
// the search, which they only read. A search that fails isn't kept, so
// the next import searches again.
func searchOnce(ctx context.Context, svc *cloudasset.Service, project string, assetTypes []string) ([]*cloudasset.ResourceSearchResult, error) {
	return searches.once(project+"\x00"+strings.Join(assetTypes, ","), func() ([]*cloudasset.ResourceSearchResult, error) {
		return searchAll(ctx, svc, project, assetTypes)
	})
}

// rejectedTypes are the asset types the search has rejected in the
// process, by where it went (an emulator, or Google) and the type: the
// searches of other projects leave them out rather than find them again.
var rejectedTypes = struct {
	sync.Mutex
	byKey map[string]bool
}{byKey: map[string]bool{}}

// rejectedKey names an asset type the search rejected, where it went.
func rejectedKey(assetType string) string {
	return os.Getenv(EndpointEnv) + "\x00" + assetType
}

// searchAll searches the project for resources of assetTypes, but for
// those the search has rejected before (see rejectedTypes). If the search
// rejects them, as it does when one can't be searched, it searches each
// half on its own, and so on, so that a few types it rejects cost a few
// searches each, not one search per type; it logs and remembers those it
// rejects. If it rejects every type, the rejection isn't about a type,
// and it fails.
func searchAll(ctx context.Context, svc *cloudasset.Service, project string, assetTypes []string) ([]*cloudasset.ResourceSearchResult, error) {
	rejectedTypes.Lock()
	searchable := slices.DeleteFunc(slices.Clone(assetTypes), func(t string) bool { return rejectedTypes.byKey[rejectedKey(t)] })
	rejectedTypes.Unlock()
	if len(searchable) == 0 {
		return nil, nil
	}
	results, err := search(ctx, svc, project, searchable)
	if !isBadRequest(err) || len(searchable) == 1 {
		return results, err
	}
	log.Printf("google: cloud_asset: the search rejects the catalogue's asset types (%v); searching them in parts", err)
	var rejected []string
	half := len(searchable) / 2
	first, err := searchParts(ctx, svc, project, searchable[:half], &rejected)
	if err != nil {
		return nil, err
	}
	second, err := searchParts(ctx, svc, project, searchable[half:], &rejected)
	if err != nil {
		return nil, err
	}
	if len(rejected) == len(searchable) {
		return nil, fmt.Errorf("searching project %s: the search rejects every asset type, each on its own too", project)
	}
	rejectedTypes.Lock()
	for _, t := range rejected {
		rejectedTypes.byKey[rejectedKey(t)] = true
	}
	rejectedTypes.Unlock()
	return append(first, second...), nil
}

// searchParts searches the project for resources of assetTypes, and, if
// the search rejects them, for each half of them on its own, adding to
// rejected the types it rejects alone, which it logs.
func searchParts(ctx context.Context, svc *cloudasset.Service, project string, assetTypes []string, rejected *[]string) ([]*cloudasset.ResourceSearchResult, error) {
	results, err := search(ctx, svc, project, assetTypes)
	switch {
	case !isBadRequest(err):
		return results, err
	case len(assetTypes) == 1:
		log.Printf("google: cloud_asset: %s can't be searched: %v", assetTypes[0], err)
		*rejected = append(*rejected, assetTypes[0])
		return nil, nil
	}
	half := len(assetTypes) / 2
	first, err := searchParts(ctx, svc, project, assetTypes[:half], rejected)
	if err != nil {
		return nil, err
	}
	second, err := searchParts(ctx, svc, project, assetTypes[half:], rejected)
	if err != nil {
		return nil, err
	}
	return append(first, second...), nil
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
	// The labels of every resource found, wherever it is, by its full
	// name: a resource's parent, such as a node pool's cluster, is in the
	// same location but may be another import's to take.
	labels := map[string]map[string]string{}
	for _, r := range results {
		if len(r.Labels) > 0 {
			labels[r.Name] = r.Labels
		}
	}
	var resources []harvest.Resource
	elsewhere := 0
	// The regions of what is elsewhere, for the import of global to name.
	var regions []string
	for _, r := range results {
		location := rootLocation(r)
		if !inRegion(location, region) {
			elsewhere++
			if in := regionOf(location); in != "" && !slices.Contains(regions, in) {
				regions = append(regions, in)
			}
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
		resources = append(resources, harvest.Record(id, assetName(r.DisplayName, id), types[0], provider, assetAttributes(r, id, projectID, labels[r.ParentFullResourceName])))
	}
	if elsewhere > 0 {
		where := "the global import"
		if region == "" {
			// --regions defaults to global alone, which leaves them out:
			// name the regions to add.
			slices.Sort(regions)
			where = "the imports of their regions, which --regions names: " + strings.Join(regions, ", ")
		}
		log.Printf("%s: cloud_asset: %d resources of project %s are in other locations, for %s", provider, elsewhere, projectID, where)
	}
	return resources
}

// assetName is the name a resource's label is made from: its display
// name, or the last segment of its ID without one. Some services' display
// names are full resource names, such as a Pub/Sub topic's
// projects/p/topics/t: those give their last segment, the resource's own
// name, as AWS and Azure labels are made from names. Other display names
// are kept whole, slashes and all, such as an alert policy's.
func assetName(displayName, id string) string {
	if strings.HasPrefix(displayName, "projects/") && lastSegment(displayName) != "" {
		return lastSegment(displayName)
	}
	if displayName != "" {
		return displayName
	}
	return lastSegment(id)
}

// withNetworkTypes are the asset types the import of global takes
// wherever their resources are, as it takes their networks: a VPC network
// and its subnetworks go into one root, where the network module, which
// takes a network with its subnetworks, can call them together.
var withNetworkTypes = []string{"compute.googleapis.com/Subnetwork"}

// rootLocation is the location whose import takes a resource the search
// found: global for one of withNetworkTypes, else its own.
func rootLocation(r *cloudasset.ResourceSearchResult) string {
	if slices.Contains(withNetworkTypes, r.AssetType) {
		return globalRegion
	}
	return r.Location
}

// PartialTypes names the types the import of the provider's region lists
// in part, through the catalogue's inventory entries: in a region, those
// of withNetworkTypes, which the import of global takes wherever they
// are; and those of the asset types the search rejected (see
// rejectedTypes), which it didn't look for.
func (p *Provider) PartialTypes() []string {
	var assetTypes []string
	if p.region.Name != "" {
		assetTypes = append(assetTypes, withNetworkTypes...)
	}
	endpoint := rejectedKey("")
	rejectedTypes.Lock()
	for key := range rejectedTypes.byKey {
		if assetType, ok := strings.CutPrefix(key, endpoint); ok {
			assetTypes = append(assetTypes, assetType)
		}
	}
	rejectedTypes.Unlock()
	if len(assetTypes) == 0 || p.catalogue == nil {
		return nil
	}
	var types []string
	for _, typ := range p.catalogue.Types() {
		e, ok := p.catalogue.Entry(typ)
		if ok && e.Inventory != nil && slices.ContainsFunc(assetTypes, func(t string) bool { return strings.EqualFold(t, e.Inventory.Type) }) {
			types = append(types, typ)
		}
	}
	return types
}

// regional matches the location of a resource in a region or zone, such
// as us-central1 or us-central1-a, rather than global or a multi-region
// such as us or nam4.
var regional = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+(-[a-z])?$`)

// regionOf is the region of a location in a region or a zone, such as
// us-central1 for us-central1-a, or "" for global or a multi-region.
func regionOf(location string) string {
	location = strings.ToLower(location)
	if !regional.MatchString(location) {
		return ""
	}
	if dash := strings.LastIndexByte(location, '-'); len(location)-dash == 2 {
		return location[:dash]
	}
	return location
}

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
// location, its display name and description, the last segment of its
// ID (name), its
// parent's ID and the searchable attributes the search returns with it,
// such as a service account's email, in snake case. Its labels go in as
// labels.<key>, for selection rules, and its parent's, which the search
// found too, as asset.parent_labels.<key>: a node pool has none of its
// own, and its cluster's tell what created it.
func assetAttributes(r *cloudasset.ResourceSearchResult, id, projectID string, parentLabels map[string]string) map[string]string {
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
	set("description", r.Description)
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
	for k, v := range parentLabels {
		attributes["asset.parent_labels."+k] = v
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
	if cat := settingsOf(g.Settings()).catalogue; cat != nil {
		return cat
	}
	return catalogue.MustBuiltin("google")
}

// SetCatalogue takes the catalogue of the import, which maps what
// cloud_asset lists.
func (p *Provider) SetCatalogue(c *catalogue.Catalogue) {
	p.catalogue = c
}
