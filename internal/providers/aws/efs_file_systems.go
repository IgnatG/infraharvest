// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
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
	parts partFailures
}

// List lists the file systems with their parts, then the access
// points, all by ID. Failing to list file systems or access points fails
// the service; the failures to read a file system's parts are returned
// together as a partial error (see partFailures).
func (g *EfsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := efs.NewFromConfig(config)
	if err := g.listFileSystems(client); err != nil {
		return errors.Join(err, g.parts.err())
	}
	if err := g.listAccessPoints(client); err != nil {
		return errors.Join(err, g.parts.err())
	}
	return g.parts.err()
}

// listFileSystems records every file system, each followed by its mount
// targets and its policy.
func (g *EfsLister) listFileSystems(client *efs.Client) error {
	p := efs.NewDescribeFileSystemsPaginator(client, &efs.DescribeFileSystemsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, fileSystem := range page.FileSystems {
			id := aws.ToString(fileSystem.FileSystemId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_efs_file_system", "aws"))
			// A call each per file system, for mount targets and policies
			// the import may not keep.
			if g.wants("aws_efs_mount_target") && !g.listMountTargets(client, id) {
				continue
			}
			if g.wants("aws_efs_file_system_policy") {
				g.addFileSystemPolicy(client, id)
			}
		}
	}
	return nil
}

// listMountTargets records the mount targets of the file system
// fileSystemID. It gathers an error and reports false when they cannot be
// read; the file system's policy is then not asked for either, as it
// would most likely fail the same way.
func (g *EfsLister) listMountTargets(client *efs.Client, fileSystemID string) bool {
	input := &efs.DescribeMountTargetsInput{FileSystemId: aws.String(fileSystemID)}
	p := efs.NewDescribeMountTargetsPaginator(client, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			g.parts.add("mount targets of "+fileSystemID, err)
			return false
		}
		for _, target := range page.MountTargets {
			id := aws.ToString(target.MountTargetId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_efs_mount_target", "aws"))
		}
	}
	return true
}

// addFileSystemPolicy records the policy of the file system fileSystemID
// by the file system's ID, with the policy document. A file system without
// a policy has none recorded; any other error is gathered.
func (g *EfsLister) addFileSystemPolicy(client *efs.Client, fileSystemID string) {
	out, err := client.DescribeFileSystemPolicy(g.Context(), &efs.DescribeFileSystemPolicyInput{FileSystemId: aws.String(fileSystemID)})
	if err != nil {
		var none *types.PolicyNotFound
		if !errors.As(err, &none) {
			g.parts.add("policy of "+fileSystemID, err)
		}
		return
	}
	g.Resources = append(g.Resources, harvest.Record(fileSystemID, fileSystemID, "aws_efs_file_system_policy", "aws", map[string]string{
		"file_system_id": fileSystemID,
		"policy":         aws.ToString(out.Policy),
	}))
}

// listAccessPoints records every access point of every file system.
func (g *EfsLister) listAccessPoints(client *efs.Client) error {
	p := efs.NewDescribeAccessPointsPaginator(client, &efs.DescribeAccessPointsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
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
