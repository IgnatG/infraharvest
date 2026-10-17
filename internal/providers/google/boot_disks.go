// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
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

// createdByGroupAttribute is recorded, as true, on an instance that a
// managed instance group created, which has the created-by metadata key
// the group sets. The catalogue leaves it out by default: the group
// deletes and recreates its instances as it scales and updates them, and
// the group itself is imported.
const createdByGroupAttribute = "asset.created_by_group"

// createdByKey is the metadata key a managed instance group sets on each
// instance it creates, to the group's URL.
const createdByKey = "created-by"

// instanceFields is what the instance list returns: each instance's URL,
// which names its project by ID however the import names it, its disks'
// sources and whether each is the boot disk, and its metadata's keys,
// without their values, which hold startup scripts; the warning of each
// zone, which tells one that can't be reached; and the next page's token,
// without which the list would stop at its first page.
const instanceFields = "items/*/instances(selfLink,disks(source,boot),metadata/items/key),items/*/warning/code,nextPageToken"

// markInstances records on each disk of resources that is the boot disk
// of an instance in results asset.boot_disk=true and the instance's ID as
// asset.boot_disk_of, and on each instance a managed instance group
// created asset.created_by_group=true. A disk attached as a data disk
// records nothing: it imports as google_compute_disk, which the
// instance's attached_disk block attaches by its source. Only a project
// whose search found instances has them listed, once (see instancesOnce),
// and only for an import that lists its instances or disks: the import of
// global lists neither. If they can't be, each disk records
// asset.boot_disk=unknown, which the catalogue leaves out by default too,
// as it may be a boot disk, the instances are imported, and the error
// says so, for the listing to fail in part (see harvest.Partial). The
// error is the context's, when it ends.
func markInstances(ctx context.Context, project string, results []*cloudasset.ResourceSearchResult, resources []harvest.Resource) error {
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
	listsDisks := slices.ContainsFunc(resources, func(r harvest.Resource) bool { return disks[r.Observed.ID] })
	listsInstances := slices.ContainsFunc(resources, func(r harvest.Resource) bool { return instances[r.Observed.ID] })
	if len(instances) == 0 || (!listsDisks && !listsInstances) {
		return nil
	}
	list, err := instancesOnce(ctx, project)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, r := range resources {
		switch id := r.Observed.ID; {
		case disks[id] && err != nil:
			r.Observed.Attributes[bootDiskAttribute] = "unknown"
		case disks[id]:
			if instance, ok := list.boot[id]; ok && instances[instance] {
				r.Observed.Attributes[bootDiskAttribute] = "true"
				r.Observed.Attributes[bootDiskOfAttribute] = instance
			}
		case instances[id] && err == nil && list.createdByGroup[id]:
			r.Observed.Attributes[createdByGroupAttribute] = "true"
		}
	}
	switch {
	case err == nil:
		return nil
	case listsDisks:
		return fmt.Errorf("the instances of project %s can't be listed, so its disks, which may be their boot disks, are left out by default, and the instances managed instance groups created can't be told apart: %w", project, err)
	default:
		return fmt.Errorf("the instances of project %s can't be listed, so the instances managed instance groups created can't be told apart: %w", project, err)
	}
}

// instanceList is what the list of a project's instances tells: the boot
// disks, by the disk's ID, with the ID of the instance each boots, such
// as projects/p/zones/z/disks/web for projects/p/zones/z/instances/web,
// and the instances a managed instance group created, by ID.
type instanceList struct {
	boot           map[string]string
	createdByGroup map[string]bool
}

// instanceLists are the instances the process has listed, by project: one
// list has every zone's instances, and one command imports a project
// region by region, so it is listed once for all of them rather than
// once for each, as searchOnce searches.
var instanceLists projectLists[instanceList]

// instancesOnce lists the project's instances, only the first time the
// process asks (see projectLists). The list is the project's instances in
// every zone, page by page, with no more of each than instanceFields. A
// zone that can't be reached fails the list: its instances would be
// missing from it.
func instancesOnce(ctx context.Context, project string) (instanceList, error) {
	return instanceLists.once(project, func() (instanceList, error) {
		svc, err := compute.NewService(ctx, clientOptions(ctx)...)
		if err != nil {
			return instanceList{}, err
		}
		list := instanceList{boot: map[string]string{}, createdByGroup: map[string]bool{}}
		var unreachable []string
		err = svc.Instances.AggregatedList(project).Fields(googleapi.Field(instanceFields)).Pages(ctx, func(page *compute.InstanceAggregatedList) error {
			for scope, scoped := range page.Items {
				if scoped.Warning != nil && scoped.Warning.Code == "UNREACHABLE" {
					unreachable = append(unreachable, scope)
				}
				for _, instance := range scoped.Instances {
					id := computeID(instance.SelfLink)
					if id == "" {
						continue
					}
					for _, disk := range instance.Disks {
						if source := computeID(disk.Source); disk.Boot && source != "" {
							list.boot[source] = id
						}
					}
					if instance.Metadata != nil && slices.ContainsFunc(instance.Metadata.Items, func(item *compute.MetadataItems) bool { return item != nil && item.Key == createdByKey }) {
						list.createdByGroup[id] = true
					}
				}
			}
			return nil
		})
		if err != nil {
			return instanceList{}, fmt.Errorf("listing the instances of project %s: %w", project, err)
		}
		if len(unreachable) > 0 {
			slices.Sort(unreachable)
			return instanceList{}, fmt.Errorf("listing the instances of project %s: %s can't be reached", project, strings.Join(unreachable, ", "))
		}
		return list, nil
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

// projectList is one project's list: its value, or its error, once it
// has been made. mu is held while it runs, so that an import that wants
// it meanwhile waits for it rather than listing too.
type projectList[T any] struct {
	mu    sync.Mutex
	done  bool
	value T
	err   error
}

// once returns what list returns for project, calling it only the first
// time the process asks. A list that fails is kept too, so that the
// imports of the project's other regions don't make the failing call
// again, each after its retries (see retryTransport): only one that its
// context ended is not, so the next import lists again.
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
		return p.value, p.err
	}
	value, err := list()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		var zero T
		return zero, err
	}
	p.value, p.err, p.done = value, err, true
	return value, err
}

// forget drops every list kept, for tests.
func (l *projectLists[T]) forget() {
	l.mu.Lock()
	l.byKey = nil
	l.mu.Unlock()
}
