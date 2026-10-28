// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// QueryService is the service, of every provider that has one, that lists
// the resource types the user names through Terraform's own list
// resources (terraform query, Terraform 1.14 and later), rather than
// through a lister of infraharvest's: for the types no lister covers. The
// importer runs Terraform for it (see WithQuery); the provider only lists
// it among its services, with a QueryLister, as an opt-in one.
const QueryService = "query"

// Query lists through Terraform's list resources what the import's query
// service lists, in the provider's scope, with ctx. A PartialError says
// it listed some of it, which it returns.
type Query func(ctx context.Context) ([]Resource, error)

// queryKey is the key of the Query in a context.
type queryKey struct{}

// WithQuery returns ctx carrying query, which the QueryLister given ctx
// (see Lister.SetContext) lists with.
func WithQuery(ctx context.Context, query Query) context.Context {
	return context.WithValue(ctx, queryKey{}, query)
}

// errNoQuery is the error of a QueryLister whose context carries no Query.
var errNoQuery = errors.New("this command can't list through Terraform's list resources")

// QueryLister is the lister of QueryService: it lists with the Query its
// context carries (see WithQuery), and applies the --where filters to what
// that returns. Its resources have no tags, so --tag filters, which the
// listers that read tags apply, pass them all.
type QueryLister struct {
	ListerBase
}

// List lists with the context's Query, keeping what it returned when it
// listed in part.
func (l *QueryLister) List() error {
	query, ok := l.Context().Value(queryKey{}).(Query)
	if !ok || query == nil {
		return errNoQuery
	}
	resources, err := query(l.Context())
	if err != nil && !IsPartial(err) {
		return err
	}
	for _, r := range resources {
		if l.keeps(r) {
			l.Resources = append(l.Resources, r)
		}
	}
	return err
}

// keeps reports whether r passes the --where filters.
func (l *QueryLister) keeps(r Resource) bool {
	for i := range l.Filters() {
		f := &l.Filters()[i]
		if f.Path == idPath || strings.HasPrefix(f.Path, tagPathPrefix) {
			continue
		}
		if !f.Keeps(r) {
			return false
		}
	}
	return true
}

// RecordIdentity returns a resource of type resourceType, listed by
// provider through Terraform's list resources, with the identity
// Terraform imports it by (see Observation.Identity) and the name
// resourceName. Its ID is the identity's ID (see tftypes.IdentityID), and
// its attributes are the identity's, for --where.
func RecordIdentity(identity map[string]string, resourceName, resourceType, provider string) Resource {
	r := Record(tftypes.IdentityID(identity), resourceName, resourceType, provider, maps.Clone(identity))
	r.Observed.Identity = maps.Clone(identity)
	return r
}

// ProviderWithQueryScope is a provider whose scope is narrower, or other,
// than what its Terraform provider's list resources return for the
// provider block the provider configures: AWS's global pseudo-region,
// whose block names us-east-1, and which covers what belongs to no region
// alone; or an Azure resource group. QueryConfig returns the arguments of
// a list block's config that narrow a list to the scope, which are set
// where the type's list resource takes them; InQueryScope reports whether
// a resource Terraform returned, by its identity, is in the scope.
type ProviderWithQueryScope interface {
	QueryConfig() map[string]string
	InQueryScope(identity map[string]string) bool
}
