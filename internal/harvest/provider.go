// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "context"

// Provider is a cloud provider that infraharvest can list resources from:
// AWS, Azure or Google Cloud. The importer drives it through these methods
// and reads what sets it apart from the others in its Profile; optional
// extras are the ProviderWith* interfaces, which callers detect with a
// type assertion.
type Provider interface {
	// Init takes the provider's positional settings, such as the region or
	// the project, and prepares the provider. Any call it makes, such as one
	// that checks the region, takes ctx, so an interrupt stops it. An error
	// aborts the import.
	Init(ctx context.Context, args []string) error
	// Lister returns a new lister for the service called serviceName,
	// given the provider's settings (see ListerOf). A lister keeps what it
	// lists while it does, so services that list at the same time each
	// have a lister of their own. It fails for a name that is no service
	// of the provider's.
	Lister(serviceName string) (Lister, error)
	// Name is the provider's short name, such as aws, azurerm or google:
	// its Terraform provider's.
	Name() string
	// Profile returns the facts that set the provider apart from the
	// others (see Profile).
	Profile() Profile
	// Services maps the name of every service the provider can list to a
	// new lister for it, as Lister returns it.
	Services() map[string]Lister
	// ProviderBlock returns the arguments of the Terraform provider block
	// that the generated configuration declares.
	ProviderBlock() map[string]interface{}
}
