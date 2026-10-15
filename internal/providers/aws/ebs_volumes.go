// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"hash/crc32"
	"strconv"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

// List lists the volumes, page by page, and their attachments.
// Telling a root volume apart takes the attached instance's root device
// name, from DescribeInstances. Any API error is returned and ends the
// listing.
func (g *EbsLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	ctx := g.Context()
	roots := rootDevices{client: client, names: map[string]string{}}

	input := &ec2.DescribeVolumesInput{
		Filters: ec2TagFilters(g.Filters(), ebsFilterServiceName),
	}
	pages := ec2.NewDescribeVolumesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for i := range page.Volumes {
			resources, err := volumeResources(ctx, &roots, &page.Volumes[i])
			if err != nil {
				return err
			}
			g.Resources = append(g.Resources, resources...)
		}
	}
	return nil
}

// volumeResources returns the resources of one volume: the volume itself
// followed by its attachments in state attached, or nothing when the volume
// is the root device of an instance it is attached to.
func volumeResources(ctx context.Context, roots *rootDevices, volume *types.Volume) ([]harvest.Resource, error) {
	volumeID := aws.ToString(volume.VolumeId)
	resources := []harvest.Resource{
		harvest.RecordBare(volumeID, volumeID, "aws_ebs_volume", "aws"),
	}
	for _, attachment := range volume.Attachments {
		instanceID := aws.ToString(attachment.InstanceId)
		device := aws.ToString(attachment.Device)
		if instanceID != "" {
			root, err := roots.name(ctx, instanceID)
			if err != nil {
				return nil, fmt.Errorf("describe instance %s of volume %s: %w", instanceID, volumeID, err)
			}
			if root == device {
				return nil, nil
			}
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
	return resources, nil
}

// rootDevices looks up the root device names of instances, asking
// DescribeInstances once per instance however many volumes are attached to
// it.
type rootDevices struct {
	client *ec2.Client
	names  map[string]string // instance ID -> root device name
}

// name returns the root device name of the instance instanceID, or "" when
// AWS does not report one.
func (r *rootDevices) name(ctx context.Context, instanceID string) (string, error) {
	if name, ok := r.names[instanceID]; ok {
		return name, nil
	}
	out, err := r.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", err
	}
	// The request names one instance, so the answer holds at most that one.
	name := ""
	for _, reservation := range out.Reservations {
		if len(reservation.Instances) > 0 {
			name = aws.ToString(reservation.Instances[0].RootDeviceName)
			break
		}
	}
	r.names[instanceID] = name
	return name, nil
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
