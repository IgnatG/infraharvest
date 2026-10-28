// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"hash/crc32"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// EbsLister lists the EBS volumes of a region as aws_ebs_volume, each by
// its volume ID, and after each volume its attachments to instances as
// aws_volume_attachment.
//
// A volume that is the root device of an instance it is attached to is left
// out, with all its attachments: Terraform manages a root volume through
// its aws_instance, not as a volume of its own. Only attachments in state
// attached are listed. An attachment is recorded by the ID Terraform gives
// it (see volumeAttachmentID), named "<instance ID>:<device>", with the
// device, volume and instance the catalogue builds its import ID from.
//
// Tag filters given with Type=ebs_volume, or with no type, are passed to
// DescribeVolumes, so only matching volumes are listed.
type EbsLister struct {
	Service
}

// ebsFilterServiceName is the Service that filters on EBS volumes are
// written with.
const ebsFilterServiceName = "ebs_volume"

// ebsInstanceBatch is how many instance IDs one DescribeInstances call
// filters on. EC2 takes up to 200 values in one filter.
const ebsInstanceBatch = 200

// List lists the volumes, page by page, and then their attachments.
// Telling a root volume apart takes the attached instances' root device
// names, which one DescribeInstances call per batch of instances returns
// (see rootDeviceNames). Any API error is returned and ends the listing.
func (g *EbsLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *EbsLister) list() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	ctx := g.Context()

	input := &ec2.DescribeVolumesInput{
		Filters: ec2TagFilters(g.Filters(), ebsFilterServiceName),
	}
	var volumes []types.Volume
	pages := ec2.NewDescribeVolumesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		volumes = append(volumes, page.Volumes...)
	}
	roots, err := rootDeviceNames(ctx, client, attachedInstanceIDs(volumes))
	if err != nil {
		return err
	}
	for i := range volumes {
		g.Resources = append(g.Resources, volumeResources(roots, &volumes[i])...)
	}
	return nil
}

// attachedInstanceIDs returns the IDs of the instances that volumes are
// attached to, each once, in the order they are first met.
func attachedInstanceIDs(volumes []types.Volume) []string {
	var ids []string
	seen := map[string]bool{}
	for i := range volumes {
		for _, attachment := range volumes[i].Attachments {
			id := aws.ToString(attachment.InstanceId)
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// rootDeviceNames returns the root device name of each of the instances
// instanceIDs, by instance ID, asking DescribeInstances once per batch of
// ebsInstanceBatch instances. The instances are named by an instance-id
// filter rather than by InstanceIds, so an instance that is gone by the
// time it is described is left out of the answer instead of failing the
// call; an instance missing from the map has no root device to compare.
func rootDeviceNames(ctx context.Context, client *ec2.Client, instanceIDs []string) (map[string]string, error) {
	names := map[string]string{}
	for batch := range slices.Chunk(instanceIDs, ebsInstanceBatch) {
		input := &ec2.DescribeInstancesInput{
			Filters: []types.Filter{{Name: aws.String("instance-id"), Values: batch}},
		}
		pages := ec2.NewDescribeInstancesPaginator(client, input, stopOnDuplicateToken)
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("describe instances %s: %w", strings.Join(batch, ", "), err)
			}
			for _, reservation := range page.Reservations {
				for _, instance := range reservation.Instances {
					names[aws.ToString(instance.InstanceId)] = aws.ToString(instance.RootDeviceName)
				}
			}
		}
	}
	return names, nil
}

// volumeResources returns the resources of one volume: the volume itself
// followed by its attachments in state attached, or nothing when the volume
// is the root device of an instance it is attached to. roots holds the
// root device names of the attached instances, by instance ID.
func volumeResources(roots map[string]string, volume *types.Volume) []harvest.Resource {
	volumeID := aws.ToString(volume.VolumeId)
	resources := []harvest.Resource{
		harvest.RecordBare(volumeID, volumeID, "aws_ebs_volume", "aws"),
	}
	for _, attachment := range volume.Attachments {
		instanceID := aws.ToString(attachment.InstanceId)
		device := aws.ToString(attachment.Device)
		if root, ok := roots[instanceID]; ok && root != "" && root == device {
			return nil
		}
		if attachment.State != types.VolumeAttachmentStateAttached {
			continue
		}
		resources = append(resources, harvest.Record(
			volumeAttachmentID(device, volumeID, instanceID),
			instanceID+":"+device,
			"aws_volume_attachment",
			"aws",
			map[string]string{
				"device_name": device,
				"volume_id":   volumeID,
				"instance_id": instanceID,
			}))
	}
	return resources
}

// volumeAttachmentID returns the ID that Terraform's AWS provider gives an
// aws_volume_attachment: "vai-" and a hash of the device name, the instance
// ID and the volume ID. Terraform builds the ID itself rather than reading
// it from AWS, so the lister must build the same one for the imported state
// to match.
func volumeAttachmentID(device, volumeID, instanceID string) string {
	return "vai-" + strconv.Itoa(terraformStringHash(device+"-"+instanceID+"-"+volumeID+"-"))
}

// terraformStringHash is the string hash of Terraform's AWS provider: the
// CRC-32 (IEEE) checksum of s as a non-negative int. On platforms where
// int has 32 bits, a checksum that does not fit is negated, as Terraform
// does.
func terraformStringHash(s string) int {
	v := int(crc32.ChecksumIEEE([]byte(s)))
	if v < 0 {
		return -v
	}
	return v
}
