// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcp

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/IgnatG/infraharvest/terraformutils"
	"google.golang.org/api/compute/v1"
)

type GCPProvider struct { //nolint
	terraformutils.Provider
	projectName  string
	region       compute.Region
	providerType string
}

// getRegion looks up a region and its zones, which the zonal listers list
// in; nothing for global. A region it can't look up fails, rather than
// leave the zonal listers nothing to list.
func getRegion(project, regionName string) (*compute.Region, error) {
	if regionName == "global" {
		return &compute.Region{}, nil
	}
	ctx := context.Background()
	computeService, err := compute.NewService(ctx, clientOptions()...)
	if err != nil {
		return nil, err
	}
	region, err := computeService.Regions.Get(project, regionName).Fields("name", "zones").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("region %s of project %s: %w", regionName, project, err)
	}
	return region, nil
}

// check projectName in env params
func (p *GCPProvider) Init(args []string) error {
	projectName := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if len(args) > 1 {
		projectName = args[1]
	}
	if projectName == "" {
		return errors.New("google cloud project name must be set")
	}
	p.projectName = projectName
	region, err := getRegion(projectName, args[0])
	if err != nil {
		return err
	}
	p.region = *region
	p.providerType = args[2]
	return nil
}

func (p *GCPProvider) GetName() string {
	if p.providerType != "" {
		return "google-" + p.providerType
	}
	return "google"
}

func (p *GCPProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("gcp: " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(map[string]interface{}{
		"region":  p.region,
		"project": p.projectName,
	})
	return nil
}

// GetGCPSupportService return map of support service for GCP
func (p *GCPProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	services := computeServices()
	services["bigQuery"] = &BigQueryGenerator{}
	services["cloudFunctions"] = &CloudFunctionsGenerator{}
	services["cloudsql"] = &CloudSQLGenerator{}
	services["cloudtasks"] = &CloudTaskGenerator{}
	services["dataProc"] = &DataprocGenerator{}
	services["dns"] = &CloudDNSGenerator{}
	services["gcs"] = &GcsGenerator{}
	services["gke"] = &GkeGenerator{}
	services["iam"] = &IamGenerator{}
	services["kms"] = &KmsGenerator{}
	services["logging"] = &LoggingGenerator{}
	services["memoryStore"] = &MemoryStoreGenerator{}
	services["monitoring"] = &MonitoringGenerator{}
	services["project"] = &ProjectGenerator{}
	services["instances"] = &InstancesGenerator{}
	services["pubsub"] = &PubsubGenerator{}
	services["schedulerJobs"] = &SchedulerJobsGenerator{}
	services["cloudbuild"] = &CloudBuildGenerator{}
	return services
}

// GetProviderData configures the provider block of the generated roots: the
// project, the region, in which regional resources imported by name are
// found, and no attribution label. The provider adds
// goog-terraform-provisioned to the labels of every resource it manages,
// so an imported resource would plan an update of its labels.
func (p GCPProvider) GetProviderData(_ ...string) map[string]interface{} {
	config := map[string]interface{}{
		"project":                         p.projectName,
		"add_terraform_attribution_label": false,
	}
	if p.region.Name != "" {
		config["region"] = p.region.Name
	}
	return map[string]interface{}{"provider": map[string]interface{}{p.GetName(): config}}
}

// notImportable are the resource types the listers list that the provider
// can't import: the import reports them as such instead of trying.
var notImportable = map[string]bool{
	"google_storage_bucket_acl":         true,
	"google_storage_default_object_acl": true,
}

// ImportID returns the ID Terraform imports r with, and false for types it
// can't import (see notImportable).
func (GCPProvider) ImportID(r terraformutils.Resource) (string, bool) {
	return r.InstanceState.ID, !notImportable[r.InstanceInfo.Type]
}

// Scope names the project and region this import covers, for the output
// layout: global for the region global.
func (p *GCPProvider) Scope(context.Context) (account, region string, err error) {
	region = p.region.Name
	if region == "" {
		region = "global"
	}
	return p.projectName, region, nil
}

// DefaultTags describes the provider's default_labels: labels it applies
// to every resource that has labels. Keys starting with goog- are Google's
// own.
func (GCPProvider) DefaultTags() (attribute, block, reservedPrefix string) {
	return "labels", "default_labels", "goog-"
}

// DefaultTagsArgument says default_labels is an argument of the provider
// block, not a block.
func (GCPProvider) DefaultTagsArgument() bool {
	return true
}
