// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudhsmv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudhsmv2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CloudHsmLister lists the service "cloudhsm": the CloudHSM v2 clusters,
// each with its HSMs.
type CloudHsmLister struct {
	Service
}

// List records every cluster by ID, each followed by its HSMs, by
// ID with the cluster's ID as an attribute. Clusters and HSMs that are
// being deleted, or are deleted, are left out: DescribeClusters keeps
// reporting deleted clusters for a while, and Terraform reads them as
// gone. Any error fails the service.
func (g *CloudHsmLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := cloudhsmv2.NewFromConfig(config)
	p := cloudhsmv2.NewDescribeClustersPaginator(client, &cloudhsmv2.DescribeClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.Clusters {
			switch cluster.State {
			case types.ClusterStateDeleteInProgress, types.ClusterStateDeleted:
				continue
			}
			clusterID := aws.ToString(cluster.ClusterId)
			g.Resources = append(g.Resources, harvest.RecordBare(clusterID, clusterID, "aws_cloudhsm_v2_cluster", "aws"))
			for _, hsm := range cluster.Hsms {
				switch hsm.State {
				case types.HsmStateDeleteInProgress, types.HsmStateDeleted:
					continue
				}
				hsmID := aws.ToString(hsm.HsmId)
				g.Resources = append(g.Resources, harvest.Record(hsmID, hsmID, "aws_cloudhsm_v2_hsm", "aws", map[string]string{
					"cluster_id": clusterID,
				}))
			}
		}
	}
	return nil
}
