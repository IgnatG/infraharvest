// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// EfsLister lists the service "efs": the EFS file systems, each with its
// mount targets and its file system policy, and the access points.
type EfsLister struct {
	Service
	// parts gathers the parts of the listing that failed.
	parts harvest.Parts
}

// List lists the file systems with their parts, then the access
// points, all by ID. Failing to list file systems or access points fails
// the service; the failures to read a file system's parts are returned
// together as a partial error (see harvest.Parts).
func (g *EfsLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *EfsLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := efs.NewFromConfig(config)
	if err := g.listFileSystems(client); err != nil {
		return g.parts.Fail(err)
	}
	if err := g.listAccessPoints(client); err != nil {
		return g.parts.Fail(err)
	}
	return g.parts.Err()
}

// listFileSystems records every file system, each followed by its mount
// targets and its policy.
func (g *EfsLister) listFileSystems(client *efs.Client) error {
	for page, err := range pagesOf(g.Context(), efs.NewDescribeFileSystemsPaginator(client, &efs.DescribeFileSystemsInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, fileSystem := range page.FileSystems {
			id := aws.ToString(fileSystem.FileSystemId)
			mark := len(g.Resources)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_efs_file_system", "aws"))
			// A call each per file system, for mount targets and policies
			// the import may not keep.
			if g.Wants("aws_efs_mount_target") {
				err := g.listMountTargets(client, id)
				if dropIfGone(&g.Resources, mark, err) {
					continue
				}
				if err != nil {
					g.parts.Add(awsCodes, "mount targets of "+id, err)
					continue
				}
			}
			if g.Wants("aws_efs_file_system_policy") {
				dropIfGone(&g.Resources, mark, g.addFileSystemPolicy(client, id))
			}
		}
	}
	return nil
}

// listMountTargets records the mount targets of the file system
// fileSystemID. It returns an error when they cannot be read, which the
// caller gathers (see List) unless it says the file system is gone; the
// file system's policy is then not asked for either, as it would most
// likely fail the same way.
func (g *EfsLister) listMountTargets(client *efs.Client, fileSystemID string) error {
	input := &efs.DescribeMountTargetsInput{FileSystemId: aws.String(fileSystemID)}
	for page, err := range pagesOf(g.Context(), efs.NewDescribeMountTargetsPaginator(client, input, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, target := range page.MountTargets {
			id := aws.ToString(target.MountTargetId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_efs_mount_target", "aws"))
		}
	}
	return nil
}

// addFileSystemPolicy records the policy of the file system fileSystemID
// by the file system's ID, with the policy document. A file system without
// a policy has none recorded; an error that says the file system is gone is
// returned, and any other error is gathered.
func (g *EfsLister) addFileSystemPolicy(client *efs.Client, fileSystemID string) error {
	out, err := client.DescribeFileSystemPolicy(g.Context(), &efs.DescribeFileSystemPolicyInput{FileSystemId: aws.String(fileSystemID)})
	if err != nil {
		var none *types.PolicyNotFound
		if errors.As(err, &none) {
			return nil
		}
		if isGone(err) {
			return err
		}
		g.parts.Add(awsCodes, "policy of "+fileSystemID, err)
		return nil
	}
	g.Resources = append(g.Resources, harvest.Record(fileSystemID, fileSystemID, "aws_efs_file_system_policy", "aws", map[string]string{
		"file_system_id": fileSystemID,
		"policy":         aws.ToString(out.Policy),
	}))
	return nil
}

// listAccessPoints records every access point of every file system.
func (g *EfsLister) listAccessPoints(client *efs.Client) error {
	for page, err := range pagesOf(g.Context(), efs.NewDescribeAccessPointsPaginator(client, &efs.DescribeAccessPointsInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, accessPoint := range page.AccessPoints {
			id := aws.ToString(accessPoint.AccessPointId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_efs_access_point", "aws"))
		}
	}
	return nil
}
