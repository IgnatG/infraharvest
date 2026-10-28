// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// ACMLister lists the service "acm": the ACM certificates.
type ACMLister struct {
	Service
}

// List records every certificate by ARN, named
// "<certificate UUID>_<domain>", the domain without a trailing dot, so
// that two certificates for one domain get different names. The domain is
// recorded as an attribute, and so are the status and the type the list
// answers already: the catalogue leaves out by status the certificates that
// failed, timed out, expired or were revoked, and an imported certificate
// is told by its type. ListCertificates asks for every key type:
// without the filter it returns only RSA_1024 and RSA_2048 certificates,
// so ECDSA and RSA_3072/4096 ones would be missing.
func (g *ACMLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *ACMLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := acm.NewFromConfig(config)
	p := acm.NewListCertificatesPaginator(client, &acm.ListCertificatesInput{Includes: &types.Filters{KeyTypes: types.KeyAlgorithm("").Values()}}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("acm: certificates: %w", err)
		}
		for _, certificate := range page.CertificateSummaryList {
			arn := aws.ToString(certificate.CertificateArn)
			domain := aws.ToString(certificate.DomainName)
			name := certificateUUID(arn) + "_" + strings.TrimSuffix(domain, ".")
			attributes := map[string]string{"domain_name": domain}
			if status := string(certificate.Status); status != "" {
				attributes["status"] = status
			}
			if certificateType := string(certificate.Type); certificateType != "" {
				attributes["type"] = certificateType
			}
			g.Resources = append(g.Resources, harvest.Record(arn, name, "aws_acm_certificate", "aws", attributes))
		}
	}
	return nil
}

// certificateUUID returns the certificate's ID, what follows the first "/"
// of its ARN ("arn:aws:acm:<region>:<account>:certificate/<UUID>"), or the
// whole ARN when it has no "/".
func certificateUUID(arn string) string {
	if _, id, found := strings.Cut(arn, "/"); found {
		return id
	}
	return arn
}
