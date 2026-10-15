// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/emr"
	"github.com/aws/aws-sdk-go-v2/service/emr/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// EmrLister lists the region's EMR clusters and security
// configurations.
type EmrLister struct {
	Service
}

// List lists the clusters, then the security configurations.
// Either list failing fails the service.
func (g *EmrLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := emr.NewFromConfig(config)
	if err := g.listClusters(g.Context(), client); err != nil {
		return err
	}
	return g.listSecurityConfigurations(g.Context(), client)
}

// emrLiveClusterStates are the states of the clusters to list. EMR reports
// terminated clusters for two months, and Terraform reads a cluster that
// is terminating or terminated as gone.
var emrLiveClusterStates = []types.ClusterState{
	types.ClusterStateStarting,
	types.ClusterStateBootstrapping,
	types.ClusterStateRunning,
	types.ClusterStateWaiting,
}

// listClusters adds each cluster that is starting, running or waiting as
// an aws_emr_cluster, imported by its ID and named after its name. The
// states are asked for, and checked in the answer too.
func (g *EmrLister) listClusters(ctx context.Context, client *emr.Client) error {
	input := &emr.ListClustersInput{ClusterStates: emrLiveClusterStates}
	p := emr.NewListClustersPaginator(client, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, cluster := range page.Clusters {
			if cluster.Status != nil && !slices.Contains(emrLiveClusterStates, cluster.Status.State) {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(cluster.Id), aws.ToString(cluster.Name), "aws_emr_cluster", "aws"))
		}
	}
	return nil
}

// listSecurityConfigurations adds each security configuration as an
// aws_emr_security_configuration, imported by its name.
func (g *EmrLister) listSecurityConfigurations(ctx context.Context, client *emr.Client) error {
	p := emr.NewListSecurityConfigurationsPaginator(client, &emr.ListSecurityConfigurationsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, sc := range page.SecurityConfigurations {
			name := aws.ToString(sc.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_emr_security_configuration", "aws"))
		}
	}
	return nil
}
