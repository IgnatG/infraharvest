// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"log"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Ec2Lister lists the EC2 instances of a region as aws_instance, each by
// its instance ID and named "<instance ID>_<Name tag>".
//
// Instances that are terminated or shutting down are left out. Each
// instance records source_dest_check "true", the value Terraform assumes
// when nothing says otherwise. Its user data isn't read: it would take a
// call per instance, nothing uses it, and Terraform's import reads it. One whose CPU options are its
// instance type's defaults, or of a type whose CPU options can't be set,
// records cpu_options "default" (see recordDefaultCPUOptions).
//
// Tag filters given with Type=instance or Type=ec2_instance, or with no
// type, are passed to DescribeInstances, so only matching instances are
// listed.
type Ec2Lister struct {
	Service
}

// ec2FilterServiceNames are the Services that filters on EC2 instances
// are written with: the Terraform type without aws_, which --id uses
// too, and the service's name.
var ec2FilterServiceNames = []string{"instance", "ec2_instance"}

// List lists the live instances, page by page. A DescribeInstances
// error is returned and ends the listing.
func (g *Ec2Lister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	ctx := g.Context()

	input := &ec2.DescribeInstancesInput{
		Filters: ec2TagFilters(g.Filters(), ec2FilterServiceNames...),
	}
	// cpuOptions has, by index in g.Resources, the CPU options of each
	// instance listed, with its type.
	cpuOptions := map[int]*types.Instance{}
	pages := ec2.NewDescribeInstancesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, reservation := range page.Reservations {
			for i := range reservation.Instances {
				instance := &reservation.Instances[i]
				if instanceGone(instance) {
					continue
				}
				id := aws.ToString(instance.InstanceId)
				g.Resources = append(g.Resources, harvest.Record(
					id,
					id+"_"+instanceNameTag(instance.Tags),
					"aws_instance",
					"aws",
					map[string]string{
						"source_dest_check": "true",
					}))
				if instance.CpuOptions != nil && instance.InstanceType != "" {
					cpuOptions[len(g.Resources)-1] = instance
				}
			}
		}
	}
	g.recordDefaultCPUOptions(ctx, client, cpuOptions)
	return nil
}

// recordDefaultCPUOptions records cpu_options "default" for the instances
// whose CPU options are what their type launches with, as
// DescribeInstanceTypes gives it, or whose type takes no CPU options:
// configuration that sets them is noise, and for a type that takes none,
// fails when the instance is created again. When the call fails, nothing
// is recorded, and the configuration keeps the CPU options.
func (g *Ec2Lister) recordDefaultCPUOptions(ctx context.Context, client *ec2.Client, instances map[int]*types.Instance) {
	if len(instances) == 0 {
		return
	}
	var instanceTypes []types.InstanceType
	for _, instance := range instances {
		if !slices.Contains(instanceTypes, instance.InstanceType) {
			instanceTypes = append(instanceTypes, instance.InstanceType)
		}
	}
	slices.Sort(instanceTypes)
	vcpus := map[types.InstanceType]*types.VCpuInfo{}
	// DescribeInstanceTypes takes up to 100 types a call.
	for batch := range slices.Chunk(instanceTypes, 100) {
		pages := ec2.NewDescribeInstanceTypesPaginator(client, &ec2.DescribeInstanceTypesInput{InstanceTypes: batch}, stopOnDuplicateToken)
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				log.Printf("aws: ec2_instance: instance types: %v; the configuration keeps the instances' CPU options", err)
				return
			}
			for _, info := range page.InstanceTypes {
				vcpus[info.InstanceType] = info.VCpuInfo
			}
		}
	}
	for i, instance := range instances {
		if info, ok := vcpus[instance.InstanceType]; ok && defaultCPUOptions(instance.CpuOptions, info) {
			g.Resources[i].Observed.Attributes["cpu_options"] = "default"
		}
	}
}

// defaultCPUOptions reports whether an instance's CPU options are its
// type's defaults, or its type takes none: it lists no valid core counts.
// An instance with AMD SEV-SNP or nested virtualisation on has options of
// its own.
func defaultCPUOptions(options *types.CpuOptions, info *types.VCpuInfo) bool {
	if info == nil {
		return false
	}
	if options.AmdSevSnp == types.AmdSevSnpSpecificationEnabled || options.NestedVirtualization == types.NestedVirtualizationSpecificationEnabled {
		return false
	}
	if len(info.ValidCores) == 0 {
		return true
	}
	return aws.ToInt32(options.CoreCount) == aws.ToInt32(info.DefaultCores) &&
		aws.ToInt32(options.ThreadsPerCore) == aws.ToInt32(info.DefaultThreadsPerCore)
}

// instanceGone reports whether an instance is terminated or shutting down,
// and so has nothing left to import. An instance without a state is kept.
func instanceGone(instance *types.Instance) bool {
	if instance.State == nil {
		return false
	}
	switch instance.State.Name {
	case types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown:
		return true
	}
	return false
}

// instanceNameTag returns the value of an instance's Name tag, or "" when
// it has none. The key is matched in any case, and when several tags match,
// the last one wins.
func instanceNameTag(tags []types.Tag) string {
	name := ""
	for _, tag := range tags {
		if strings.EqualFold(aws.ToString(tag.Key), "name") {
			name = aws.ToString(tag.Value)
		}
	}
	return name
}
