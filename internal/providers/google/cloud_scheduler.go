// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"fmt"
	"log"

	"google.golang.org/api/cloudscheduler/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// schedulerJobType is the Terraform type of a Cloud Scheduler job.
const schedulerJobType = "google_cloud_scheduler_job"

// schedulerJobService lists the project's Cloud Scheduler jobs.
const schedulerJobService = "schedulerJobs"

// SchedulerJobsLister lists the project's Cloud Scheduler jobs in the
// import's region, the schedulerJobs service. Cloud Asset Inventory has no
// asset type for them, so cloud_asset can't find them. Jobs belong to a
// region, so the import of global lists none. Listing needs
// cloudscheduler.jobs.list on the project, which roles/cloudscheduler.viewer
// and roles/viewer grant.
type SchedulerJobsLister struct {
	harvest.ListerOf[listerSettings]
}

// List lists the jobs, from every page. A project without the
// Cloud Scheduler API enabled, or a region the service isn't offered in,
// has no jobs to list: that is logged, and lists nothing. Any other
// failure is an error.
func (g *SchedulerJobsLister) List() error {
	region := g.Settings().region
	if region.Name == "" {
		return nil
	}
	project, err := g.Settings().projectID()
	if err != nil {
		return err
	}
	ctx := g.Context()
	svc, err := cloudscheduler.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return err
	}
	parent := "projects/" + project + "/locations/" + region.Name
	var resources []harvest.Resource
	err = svc.Projects.Locations.Jobs.List(parent).Pages(ctx, func(page *cloudscheduler.ListJobsResponse) error {
		for _, job := range page.Jobs {
			name := lastSegment(job.Name)
			resources = append(resources, harvest.Record(job.Name, name, schedulerJobType, g.Provider(), map[string]string{
				"name":    name,
				"project": project,
				"region":  region.Name,
			}))
		}
		return nil
	})
	if err != nil {
		if nothingToList(err) {
			log.Printf("%s: schedulerJobs: no Cloud Scheduler jobs listed in %s: %v", g.Provider(), parent, err)
			return nil
		}
		return fmt.Errorf("listing the Cloud Scheduler jobs in %s: %w", parent, err)
	}
	g.Resources = resources
	return nil
}
