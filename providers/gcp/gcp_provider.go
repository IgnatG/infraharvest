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
	"log"
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

func getRegion(project, regionName string) *compute.Region {
	if regionName == "global" {
		return &compute.Region{}
	}
	computeService, err := compute.NewService(context.Background())
	if err != nil {
		log.Println(err)
		return &compute.Region{}
	}
	regionsGetCall := computeService.Regions.Get(project, regionName).Fields("name", "zones")
	region, err := regionsGetCall.Do()
	if err != nil {
		log.Println(err)
		return &compute.Region{}
	}
	return region
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
	p.region = *getRegion(projectName, args[0])
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
	services := ComputeServices
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

func (p GCPProvider) GetProviderData(arg ...string) map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			p.GetName(): map[string]interface{}{
				"project": p.projectName,
			},
		},
	}
}
