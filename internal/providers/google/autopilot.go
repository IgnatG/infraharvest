// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/container/v1"
	"google.golang.org/api/googleapi"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// nodePoolAssetType is the asset type of a GKE node pool, which the
// search returns for google_container_node_pool.
const nodePoolAssetType = "container.googleapis.com/NodePool"

// A node pool the search found records whether its cluster is an
// Autopilot cluster, as asset.parent_autopilot: true, or unknown when the
// project's clusters couldn't be listed. The catalogue leaves both out by
// default: GKE manages an Autopilot cluster's node pools, its default
// pool among them, and creates and deletes them itself. The search
// doesn't say whether a cluster is Autopilot.
const parentAutopilotAttribute = "asset.parent_autopilot"

// autopilotFields is what the cluster list returns: each cluster's name
// and location and whether it is an Autopilot cluster, and the zones that
// couldn't be listed.
const autopilotFields = "clusters(name,location,autopilot/enabled),missingZones"

// markAutopilotPools records on each node pool of resources whose cluster
// is an Autopilot cluster asset.parent_autopilot=true. Only an import
// that lists node pools has the project's clusters listed, once (see
// autopilotClustersOnce): the import of global lists none. If they can't
// be, each node pool records asset.parent_autopilot=unknown, which the
// catalogue leaves out by default too, as GKE may manage it, and the
// error says so, for the listing to fail in part (see harvest.Partial).
// The error is the context's, when it ends.
func markAutopilotPools(ctx context.Context, project string, results []*cloudasset.ResourceSearchResult, resources []harvest.Resource) error {
	// The cluster of each node pool, by the pool's ID.
	pools := map[string]string{}
	for _, r := range results {
		if r.AssetType != nodePoolAssetType || strings.Contains(strings.ToUpper(r.State), "DELET") {
			continue
		}
		parent := r.ParentFullResourceName
		if parent == "" {
			parent, _, _ = strings.Cut(r.Name, "/nodePools/")
		}
		if cluster := clusterKey(assetID(parent, r.Project, project)); cluster != "" {
			pools[assetID(r.Name, r.Project, project)] = cluster
		}
	}
	listed := slices.ContainsFunc(resources, func(r harvest.Resource) bool {
		_, ok := pools[r.Observed.ID]
		return ok
	})
	if !listed {
		return nil
	}
	autopilot, err := autopilotClustersOnce(ctx, project)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, r := range resources {
		cluster, ok := pools[r.Observed.ID]
		if !ok {
			continue
		}
		if err != nil {
			r.Observed.Attributes[parentAutopilotAttribute] = "unknown"
			continue
		}
		if autopilot[cluster] {
			r.Observed.Attributes[parentAutopilotAttribute] = "true"
		}
	}
	if err != nil {
		return fmt.Errorf("the GKE clusters of project %s can't be listed, so its node pools, which GKE may manage, are left out by default: %w", project, err)
	}
	return nil
}

// clusterKey names a cluster by its location and name, such as
// us-central1/prod, from its ID, which has locations/<location> or, for
// some zonal clusters, zones/<zone>, as the cluster list's location is
// either. An ID that isn't a cluster's gives "".
func clusterKey(id string) string {
	segments := strings.Split(id, "/")
	n := len(segments)
	if n < 4 || segments[n-2] != "clusters" || (segments[n-4] != "locations" && segments[n-4] != "zones") {
		return ""
	}
	return segments[n-3] + "/" + segments[n-1]
}

// autopilotClusterLists are the Autopilot clusters the process has listed,
// by project: one list has every location's clusters, and one command
// imports a project region by region, so it is listed once for all of
// them rather than once for each, as searchOnce searches.
var autopilotClusterLists projectLists[map[string]bool]

// autopilotClustersOnce returns the project's Autopilot clusters, by
// clusterKey, listing them only the first time the process asks (see
// projectLists). The list is the project's clusters in every location,
// in one response, with no more of each than autopilotFields. A zone that
// can't be listed fails the list: its clusters would be missing from it.
func autopilotClustersOnce(ctx context.Context, project string) (map[string]bool, error) {
	return autopilotClusterLists.once(project, func() (map[string]bool, error) {
		svc, err := container.NewService(ctx, clientOptions(ctx)...)
		if err != nil {
			return nil, err
		}
		list, err := svc.Projects.Locations.Clusters.List("projects/" + project + "/locations/-").Fields(googleapi.Field(autopilotFields)).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("listing the GKE clusters of project %s: %w", project, err)
		}
		if len(list.MissingZones) > 0 {
			missing := slices.Sorted(slices.Values(list.MissingZones))
			return nil, fmt.Errorf("listing the GKE clusters of project %s: %s can't be listed", project, strings.Join(missing, ", "))
		}
		autopilot := map[string]bool{}
		for _, cluster := range list.Clusters {
			if cluster.Autopilot != nil && cluster.Autopilot.Enabled {
				autopilot[cluster.Location+"/"+cluster.Name] = true
			}
		}
		return autopilot, nil
	})
}
