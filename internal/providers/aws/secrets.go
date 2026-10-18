// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// SecretsManagerLister lists a region's Secrets Manager secrets, by
// ARN, which Terraform imports them with, and named by their names.
type SecretsManagerLister struct {
	Service
}

// List lists the secrets people manage. A secret with an owning
// service, such as the master password RDS manages for a database, is
// that service's to create, rotate and delete, so it is left out, as is a
// secret scheduled for deletion, which Terraform treats as gone. The
// request asks for none of those, and one answered anyway is skipped.
// Errors are returned.
func (g *SecretsManagerLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := secretsmanager.NewFromConfig(config)
	input := &secretsmanager.ListSecretsInput{IncludePlannedDeletion: aws.Bool(false)}
	p := secretsmanager.NewListSecretsPaginator(svc, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, secret := range page.SecretList {
			if aws.ToString(secret.OwningService) != "" || secret.DeletedDate != nil {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(secret.ARN), aws.ToString(secret.Name), "aws_secretsmanager_secret", "aws"))
		}
	}
	return nil
}
