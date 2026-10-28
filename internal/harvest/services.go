// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "context"

// Lister lists one service of a provider. The discovery driver calls, in
// order: the provider's Select, Selected, SetFilters with the user's
// filters, SetContext, List, Prune and Listed.
//
// Concrete listers embed ListerBase, which implements every method but
// List, the listing itself.
type Lister interface {
	// List lists the service's resources from the cloud and stores them in
	// the lister. An error fails the service, and what it listed is left
	// out, unless it is a PartialError (see Partial): the lister listed
	// all it could, and what it listed is kept.
	List() error
	// Listed returns the resources the lister holds.
	Listed() []Resource
	// SetListed replaces the resources the lister holds.
	SetListed(resources []Resource)
	// SetFilters replaces the lister's filters: those of --id, --tag and
	// --where.
	SetFilters(filters []Filter)
	// Configure sets the service's name, its provider's name and whether
	// to log verbosely, all at once. The provider's own settings, such as
	// the region, it passes typed (see ListerOf).
	Configure(config ListerConfig)
	// Provider returns the provider's short name.
	Provider() string
	// Name returns the service's name.
	Name() string
	// Prune applies the ID filters to the listed resources and drops
	// duplicates.
	Prune()
	// SetContext sets the context of the lister's API calls, which is
	// cancelled when the user interrupts and carries the listing deadline.
	SetContext(ctx context.Context)
}

// ListerConfig is what a provider's Select hands every lister: the
// service's name, the provider's short name and whether to log verbosely.
type ListerConfig struct {
	Service  string
	Provider string
	Verbose  bool
}

// ListerBase is the state every lister has: the service's name, the
// resources it listed, the name of its provider, its filters and whether
// to log verbosely. Concrete listers embed it, or ListerOf with the
// provider's settings, append to Resources directly and read the rest
// through its methods.
//
// The zero value is ready to use.
type ListerBase struct {
	Resources []Resource

	name     string
	provider string
	filters  []Filter
	verbose  bool

	// ctx is the context set by SetContext, nil until then.
	ctx context.Context
}

// SetContext sets the context that Context returns.
func (s *ListerBase) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// Context returns the context for the lister's API calls: the one set by
// SetContext, or context.Background when none (or nil) was set. It is never
// nil.
func (s *ListerBase) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Configure sets the name, the provider's name and the verbosity from
// config.
func (s *ListerBase) Configure(config ListerConfig) {
	s.name = config.Service
	s.provider = config.Provider
	s.verbose = config.Verbose
}

// Provider returns the provider's name.
func (s *ListerBase) Provider() string {
	return s.provider
}

// Verbose reports whether the lister logs verbosely.
func (s *ListerBase) Verbose() bool {
	return s.verbose
}

// Filters returns the filters, nil until SetFilters is called.
func (s *ListerBase) Filters() []Filter {
	return s.filters
}

// SetFilters replaces the filters with a copy of filters, in order. They
// are never nil afterwards, even when filters is.
func (s *ListerBase) SetFilters(filters []Filter) {
	s.filters = append(make([]Filter, 0, len(filters)), filters...)
}

// Name returns the service's name.
func (s *ListerBase) Name() string {
	return s.name
}

// Prune applies the ID filters and drops duplicate resources, see prune.
func (s *ListerBase) Prune() {
	prune(s)
}

// Listed returns Resources.
func (s *ListerBase) Listed() []Resource {
	return s.Resources
}

// SetListed sets Resources to resources itself, not a copy.
func (s *ListerBase) SetListed(resources []Resource) {
	s.Resources = resources
}

// ListerOf is ListerBase with the settings a provider passes each of its
// listers, such as the region or the subscription, of a type S the
// provider owns: its listers embed it, and its Select passes the settings
// to the lister it makes (see SettingsTaker), typed, so that neither
// side reads them by name.
//
// The zero value is ready to use, with S's zero value as the settings.
type ListerOf[S any] struct {
	ListerBase
	settings S
}

// Settings returns the settings SetSettings set.
func (s *ListerOf[S]) Settings() S {
	return s.settings
}

// SetSettings replaces the settings.
func (s *ListerOf[S]) SetSettings(settings S) {
	s.settings = settings
}

// SettingsTaker is a lister that takes settings of type S, as one that
// embeds ListerOf[S] does. A provider's Select passes them to the lister
// it makes when it is one: a lister of another kind, such as QueryLister,
// takes none.
type SettingsTaker[S any] interface {
	SetSettings(settings S)
}
