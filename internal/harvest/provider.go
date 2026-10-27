// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "context"

// Provider is a cloud provider that infraharvest can list resources from:
// AWS, Azure or Google Cloud. The importer drives it through these methods
// and reads what sets it apart from the others in its Profile; optional
// extras are the ProviderWith* interfaces, which callers detect with a
// type assertion.
//
// Registry makes new providers of an implementation's dynamic type with
// reflect.New, so implementations are pointer types whose zero value is
// ready for Init.
type Provider interface {
	// Init takes the provider's positional settings, such as the region or
	// the project, and prepares the provider. Any call it makes, such as one
	// that checks the region, takes ctx, so an interrupt stops it. An error
	// aborts the import.
	Init(ctx context.Context, args []string) error
	// Select makes the service called serviceName the provider's current
	// one, which Selected then returns, and passes verbose on to it.
	Select(serviceName string, verbose bool) error
	// Name is the provider's short name, such as aws, azurerm or google:
	// its Terraform provider's.
	Name() string
	// Profile returns the facts that set the provider apart from the
	// others (see Profile).
	Profile() Profile
	// Selected returns the current service's lister, set by Select.
	Selected() Lister
	// Services maps the name of every service the provider can list to a
	// new lister for it.
	Services() map[string]Lister
	// ProviderBlock returns the arguments of the Terraform provider block
	// that the generated configuration declares.
	ProviderBlock() map[string]interface{}
}

// ProviderBase holds a provider's current lister. Concrete providers embed
// it, set Current in their Select and get Selected from it. It provides
// none of the other Provider methods on purpose, so that a provider missing
// one fails to compile instead of inheriting a silent default.
type ProviderBase struct {
	Current Lister
}

// Selected returns the Current field as it is, nil until a service is
// selected.
func (p *ProviderBase) Selected() Lister {
	return p.Current
}
