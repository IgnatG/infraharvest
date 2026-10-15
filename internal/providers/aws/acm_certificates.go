// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
)

// ACMLister lists the service "acm": the ACM certificates.
type ACMLister struct {
	Service
}

// List records every certificate by ARN, named
// "<certificate UUID>_<domain>", the domain without a trailing dot, so
// that two certificates for one domain get different names. The domain is
// recorded as an attribute. ListCertificates is called without filters,
// so it returns what AWS returns by default. A listing error is logged,
// not returned: the certificates read before it are kept.
func (g *ACMLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := acm.NewFromConfig(config)
	p := acm.NewListCertificatesPaginator(client, &acm.ListCertificatesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: acm: certificates: %v", err)
			return nil
		}
		for _, certificate := range page.CertificateSummaryList {
			arn := aws.ToString(certificate.CertificateArn)
			domain := aws.ToString(certificate.DomainName)
			name := certificateUUID(arn) + "_" + strings.TrimSuffix(domain, ".")
			g.Resources = append(g.Resources, harvest.Record(arn, name, "aws_acm_certificate", "aws", map[string]string{
				"domain_name": domain,
			}))
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
