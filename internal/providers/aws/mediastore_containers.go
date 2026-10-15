// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/mediastore"
	"github.com/aws/aws-sdk-go-v2/service/mediastore/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// MediaStoreLister lists a region's MediaStore containers by name,
// which is what Terraform imports them with. MediaStore has reached end of
// support, but containers that still exist can be imported.
type MediaStoreLister struct {
	Service
}

// List lists the containers page by page and returns the first
// error. A container being deleted is left out, as it will be gone before
// Terraform could import it; one being created is listed.
func (g *MediaStoreLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := mediastore.NewFromConfig(config)
	p := mediastore.NewListContainersPaginator(svc, &mediastore.ListContainersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, container := range page.Containers {
			if container.Status == types.ContainerStatusDeleting {
				continue
			}
			name := aws.ToString(container.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_media_store_container", "aws"))
		}
	}
	return nil
}
