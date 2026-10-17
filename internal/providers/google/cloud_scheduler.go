// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

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
	harvest.ListerBase
}

// List lists the jobs, from every page. A project without the
// Cloud Scheduler API enabled, or a region the service isn't offered in,
// has no jobs to list: that is logged, and lists nothing. Any other
// failure is an error.
func (g *SchedulerJobsLister) List() error {
	region, _ := g.Settings()["region"].(compute.Region)
	if region.Name == "" {
		return nil
	}
	project, err := projectSetting(g.Settings())
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

// nothingToList reports whether a failed list means there is nothing
// there rather than that listing failed: the API isn't enabled in the
// project (403 with the reason SERVICE_DISABLED, or accessNotConfigured in
// the older error format), or the location doesn't offer the service (404,
// or 400 for a location the API doesn't know). A 403 for a missing
// permission is a failure.
func nothingToList(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case http.StatusNotFound, http.StatusBadRequest:
		return true
	case http.StatusForbidden:
		return serviceDisabled(apiErr)
	}
	return false
}

// isServiceDisabled reports whether err says the API isn't enabled in the
// project (see serviceDisabled).
func isServiceDisabled(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusForbidden && serviceDisabled(apiErr)
}

// serviceDisabled reports whether an API error says the API isn't enabled
// in the project. The reason is in the error's google.rpc.ErrorInfo
// detail, or, in the older format, in one of its error items.
func serviceDisabled(apiErr *googleapi.Error) bool {
	for _, item := range apiErr.Errors {
		if item.Reason == "accessNotConfigured" {
			return true
		}
	}
	for _, detail := range apiErr.Details {
		if info, ok := detail.(map[string]interface{}); ok && info["reason"] == "SERVICE_DISABLED" {
			return true
		}
	}
	return false
}
