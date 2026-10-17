// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The asset types of instances and disks, which the search returns for
// google_compute_instance and for google_compute_disk and
// google_compute_region_disk.
const (
	instanceAssetType = "compute.googleapis.com/Instance"
	diskAssetType     = "compute.googleapis.com/Disk"
)

// A disk the search found records whether it is the boot disk of an
// instance the search found too, as asset.boot_disk: true, with the
// instance's ID as asset.boot_disk_of, or unknown when the instances'
// disks couldn't be listed. The catalogue leaves both out by default: the
// instance manages its boot disk in its boot_disk block, so the disk
// would have two owners.
const (
	bootDiskAttribute   = "asset.boot_disk"
	bootDiskOfAttribute = "asset.boot_disk_of"
)

// bootDiskFields is what the instance list returns: each instance's name
// and its disks' sources and whether each is the boot disk, the warning of
// each zone, which tells one that can't be reached, and the next page's
// token, without which the list would stop at its first page.
const bootDiskFields = "items/*/instances(name,disks(source,boot)),items/*/warning/code,nextPageToken"

// markBootDisks records on each disk of resources that is the boot disk
// of an instance in results asset.boot_disk=true and the instance's ID as
// asset.boot_disk_of. A disk attached as a data disk records nothing: it
// imports as google_compute_disk, which the instance's attached_disk block
// attaches by its source. Only a project whose search found instances has
// its instances' disks listed, once (see bootDisksOnce), and only for an
// import that lists disks: the import of global lists none. If they can't
// be, that is logged, and each disk records asset.boot_disk=unknown, which
// the catalogue leaves out by default too, as it may be a boot disk. The
// error is the context's, when it ends.
func markBootDisks(ctx context.Context, provider, project string, results []*cloudasset.ResourceSearchResult, resources []harvest.Resource) error {
	instances, disks := map[string]bool{}, map[string]bool{}
	for _, r := range results {
		if strings.Contains(strings.ToUpper(r.State), "DELET") {
			continue
		}
		switch r.AssetType {
		case instanceAssetType:
			instances[assetID(r.Name, r.Project, project)] = true
		case diskAssetType:
			disks[assetID(r.Name, r.Project, project)] = true
		}
	}
	listed := slices.ContainsFunc(resources, func(r harvest.Resource) bool { return disks[r.Observed.ID] })
	if len(instances) == 0 || !listed {
		return nil
	}
	boot, err := bootDisksOnce(ctx, project)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("%s: cloud_asset: the disks of project %s's instances can't be listed, so its disks, which may be their boot disks, are left out by default: %v", provider, project, err)
	}
	for _, r := range resources {
		if !disks[r.Observed.ID] {
			continue
		}
		if err != nil {
			r.Observed.Attributes[bootDiskAttribute] = "unknown"
			continue
		}
		if instance, ok := boot[r.Observed.ID]; ok && instances[instance] {
			r.Observed.Attributes[bootDiskAttribute] = "true"
			r.Observed.Attributes[bootDiskOfAttribute] = instance
		}
	}
	return nil
}

// bootDiskLists are the boot disks the process has listed, by project:
// one list has every zone's instances, and one command imports a project
// region by region, so it is listed once for all of them rather than
// once for each, as searchOnce searches.
var bootDiskLists projectLists[map[string]string]

// bootDisksOnce returns the boot disks of the project's instances, by the
// disk's ID, with the ID of the instance each boots, such as
// projects/p/zones/z/disks/web for projects/p/zones/z/instances/web,
// listing them only the first time the process asks (see projectLists).
// The list is the project's instances in every zone, page by page, with
// no more of each than bootDiskFields. A zone that can't be reached fails
// the list: its instances' boot disks would be missing from it.
func bootDisksOnce(ctx context.Context, project string) (map[string]string, error) {
	return bootDiskLists.once(project, func() (map[string]string, error) {
		svc, err := compute.NewService(ctx, clientOptions(ctx)...)
		if err != nil {
			return nil, err
		}
		boot := map[string]string{}
		var unreachable []string
		err = svc.Instances.AggregatedList(project).Fields(googleapi.Field(bootDiskFields)).Pages(ctx, func(page *compute.InstanceAggregatedList) error {
			for scope, list := range page.Items {
				if list.Warning != nil && list.Warning.Code == "UNREACHABLE" {
					unreachable = append(unreachable, scope)
				}
				for _, instance := range list.Instances {
					id := "projects/" + project + "/" + scope + "/instances/" + instance.Name
					for _, disk := range instance.Disks {
						if source := computeID(disk.Source); disk.Boot && source != "" {
							boot[source] = id
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing the instances of project %s: %w", project, err)
		}
		if len(unreachable) > 0 {
			slices.Sort(unreachable)
			return nil, fmt.Errorf("listing the instances of project %s: %s can't be reached", project, strings.Join(unreachable, ", "))
		}
		return boot, nil
	})
}

// computeID is the ID of a resource that a Compute Engine URL, such as
// https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d, names:
// projects/p/zones/z/disks/d, as the search names it. A URL with no
// project gives "".
func computeID(url string) string {
	_, path, ok := strings.Cut(url, "/projects/")
	if !ok {
		return ""
	}
	return "projects/" + path
}

// projectLists are lists of one kind that the process has made, by
// project and where they went (an emulator, or Google), each made once:
// the imports of a project's regions and of global share a list, which
// they only read.
type projectLists[T any] struct {
	mu    sync.Mutex
	byKey map[string]*projectList[T]
}

// projectList is one project's list: its value once it has succeeded. mu
// is held while it runs, so that an import that wants it meanwhile waits
// for it rather than listing too.
type projectList[T any] struct {
	mu    sync.Mutex
	done  bool
	value T
}

// once returns what list returns for project, calling it only the first
// time the process asks. A list that fails isn't kept, so the next import
// lists again.
func (l *projectLists[T]) once(project string, list func() (T, error)) (T, error) {
	key := os.Getenv(EndpointEnv) + "\x00" + project
	l.mu.Lock()
	if l.byKey == nil {
		l.byKey = map[string]*projectList[T]{}
	}
	p, ok := l.byKey[key]
	if !ok {
		p = &projectList[T]{}
		l.byKey[key] = p
	}
	l.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.value, nil
	}
	value, err := list()
	if err != nil {
		var zero T
		return zero, err
	}
	p.value, p.done = value, true
	return value, nil
}

// forget drops every list kept, for tests.
func (l *projectLists[T]) forget() {
	l.mu.Lock()
	l.byKey = nil
	l.mu.Unlock()
}
