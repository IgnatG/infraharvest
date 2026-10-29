// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import "github.com/IgnatG/infraharvest/internal/harvest"

// traits returns what Google Cloud's resources and the values that
// identify them are like: paths that start with projects/ and service
// accounts' emails identify a resource on their own; some APIs name a
// Compute Engine resource by its self link on the service's own host, as
// a DNS zone's network_url does
// (https://compute.googleapis.com/compute/v1/... for
// https://www.googleapis.com/compute/v1/...); the attributes the provider
// computes that identify their resources are such as a service account's
// unique ID and a zone's DNS name; the provider labels what Terraform
// creates goog-terraform-provisioned=true; and resources take labels, of
// which terraform_labels and effective_labels have the provider's and
// every label too.
func traits() harvest.Traits {
	return harvest.Traits{
		IDPrefixes:         []string{"projects/"},
		IDSuffixes:         []string{".gserviceaccount.com"},
		SelfLinkAliases:    map[string]string{"https://www.googleapis.com/compute/": "https://compute.googleapis.com/compute/"},
		IdentityAttributes: []string{"key_id", "dns_name", "domain_name", "fqdn", "client_id", "unique_id"},
		OwnerTags:          []harvest.OwnerTag{{Key: "goog-terraform-provisioned", Value: "true", Tool: "Terraform"}},
		TagAttributes:      []string{"tags", "labels", "terraform_labels", "effective_labels"},
	}
}
