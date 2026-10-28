// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"
	"fmt"
	"slices"
)

// Lister lists one service of a provider, in one call. The discovery
// driver gets it from the provider's Lister, calls List with the context
// of the listing, which carries its deadline, and the settings of the
// import, and then applies the ID filters and drops duplicates (see
// Prune).
type Lister interface {
	// List lists the service's resources from the cloud and returns them.
	// An error fails the service, and what it listed is left out, unless
	// it is a PartialError (see Partial): the lister listed all it could,
	// and what it listed is kept.
	//
	// Every API call it makes takes ctx. A lister keeps what it lists
	// while it does, so List is called once on each lister the provider
	// makes.
	List(ctx context.Context, s Settings) ([]Resource, error)
}

// Settings are what the import gives each lister it calls, the same for
// all of a provider's: the service's name, the provider's short name, how
// verbosely to log, the user's filters and the types the import leaves
// out. What only one provider's listers need, such as the region, they get
// typed from their provider (see ListerOf).
type Settings struct {
	// Service is the service's name and Provider the provider's short
	// name.
	Service, Provider string
	// Verbose says whether to log verbosely.
	Verbose bool
	// Filters are those of --id, --tag and --where.
	Filters []Filter
	// Unwanted reports the types the import leaves out whatever is listed
	// (see selection.Unwanted), such as aws_iam_access_key unless the
	// selection file includes some, and is safe for concurrent use; nil
	// wants every type.
	Unwanted func(typ string) bool
}

// Wants reports whether the import may keep resources of typ: a lister
// skips the calls that list only resources of a type it doesn't want.
func (s Settings) Wants(typ string) bool {
	return s.Unwanted == nil || !s.Unwanted(typ)
}

// WantsAny reports whether the import may keep resources of any of types:
// a call that lists several types is skipped only when it wants none.
func (s Settings) WantsAny(types ...string) bool {
	return slices.ContainsFunc(types, s.Wants)
}

// ListerOf is the state a lister has while it lists, with the settings a
// provider passes each of its listers, such as the region or the
// subscription, of a type S the provider owns: its listers embed it, and
// the provider's Lister passes the settings to the lister it makes (see
// SettingsTaker), typed, so that neither side reads them by name. A lister
// appends what it lists to Resources, and its List is Run of its listing.
//
// The zero value is ready to use, with S's zero value as the settings.
type ListerOf[S any] struct {
	// Resources is what the lister has listed so far, which Run returns.
	Resources []Resource

	provider S
	settings Settings

	// ctx is the context Run was given, nil until then.
	ctx context.Context
}

// Run is the List of a lister whose listing is list: it makes ctx and s
// what Context and the accessors of the settings return, calls list, and
// returns Resources with what list returned.
func (l *ListerOf[S]) Run(ctx context.Context, s Settings, list func() error) ([]Resource, error) {
	l.Begin(ctx, s)
	err := list()
	return l.Resources, err
}

// Begin makes ctx and s what Context and the accessors of the settings
// return, for the calls a lister makes outside Run.
func (l *ListerOf[S]) Begin(ctx context.Context, s Settings) {
	l.ctx = ctx
	l.settings = s
}

// Context returns the context for the lister's API calls: the one Run was
// given, or context.Background before. It is never nil.
func (l *ListerOf[S]) Context() context.Context {
	if l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

// Name returns the service's name.
func (l *ListerOf[S]) Name() string {
	return l.settings.Service
}

// Provider returns the provider's short name.
func (l *ListerOf[S]) Provider() string {
	return l.settings.Provider
}

// Verbose reports whether the lister logs verbosely.
func (l *ListerOf[S]) Verbose() bool {
	return l.settings.Verbose
}

// Filters returns the filters of --id, --tag and --where.
func (l *ListerOf[S]) Filters() []Filter {
	return l.settings.Filters
}

// Wants reports whether the import may keep resources of typ (see
// Settings.Wants).
func (l *ListerOf[S]) Wants(typ string) bool {
	return l.settings.Wants(typ)
}

// WantsAny reports whether the import may keep resources of any of types
// (see Settings.WantsAny).
func (l *ListerOf[S]) WantsAny(types ...string) bool {
	return l.settings.WantsAny(types...)
}

// Settings returns the settings SetSettings set, the provider's.
func (l *ListerOf[S]) Settings() S {
	return l.provider
}

// SetSettings replaces the provider's settings.
func (l *ListerOf[S]) SetSettings(settings S) {
	l.provider = settings
}

// SettingsTaker is a lister that takes settings of type S, as one that
// embeds ListerOf[S] does. ListerFor passes them to the lister it makes
// when it is one: a lister of another kind, such as QueryLister, takes
// none.
type SettingsTaker[S any] interface {
	SetSettings(settings S)
}

// ListerFor returns the lister of the service called name that newLister
// makes, given the provider's settings when it takes them (see
// SettingsTaker), for a provider's Lister. newLister returns nil for a
// name that is no service of the provider's command, which fails with the
// error that points to its list of services.
func ListerFor[S any](command, name string, newLister func(name string) Lister, settings S) (Lister, error) {
	lister := newLister(name)
	if lister == nil {
		return nil, fmt.Errorf("not a service of %s; \"infraharvest services %s\" lists them", command, command)
	}
	if l, ok := lister.(SettingsTaker[S]); ok {
		l.SetSettings(settings)
	}
	return lister, nil
}
