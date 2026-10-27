// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// The interfaces in this file are capabilities of a provider command's
// scope: what the command's own flags say of where to list and what to
// import, such as AWS's --profile, --accounts and --cfn-stack, or Azure's
// --resource-group. The command builds the scope and hands it to each
// Import call (importer.Options.ProviderScope), and reads it with its
// provider, through the call's args. To the importer the scope is opaque:
// it checks for each of these with a type assertion, and does without
// what the scope lacks, as it does for a Provider's capabilities.

// StackScoped is a scope that may take only the resources of stacks the
// user names, such as the CloudFormation stacks of AWS's --cfn-stack.
// The stacks' resources say which services to list and what to import, so
// an import scoped to them needs neither --services nor a selection file;
// it keeps their resources in the roots, where the stacks' templates find
// them (see stackintent), unless --modules says otherwise; and it tells
// which roots the environments of a stack merge into before it generates
// them, as only those that don't merge take the stacks' templates.
type StackScoped interface {
	// StackScoped reports whether the import takes the stacks' resources
	// alone.
	StackScoped() bool
}

// ManagedStateScope is a scope that reads the state --managed-state names
// with settings of its own, such as AWS's --profile for state in S3.
type ManagedStateScope interface {
	// ManagedStateSource returns source, a --managed-state source, with
	// the scope's settings, unless it is of another kind or names its own.
	ManagedStateSource(source string) string
}

// PartialListingScope is a scope whose listing may leave out what the
// cloud still has, such as AWS's --resource-explorer, which skips the
// services Resource Explorer's index shows nothing of: what it doesn't
// list is unknown, not gone.
type PartialListingScope interface {
	// ListsInPart says why the listing may leave something out, for the
	// report; "" when it lists in full.
	ListsInPart() string
}

// GroupScope is a scope that imports one group of its account,
// subscription or project, such as Azure's --resource-group: the root the
// environments of a stack merge into reads each environment's group as
// it does its account and region.
type GroupScope interface {
	// ScopeGroup names the group; "" for the whole account, subscription
	// or project.
	ScopeGroup() string
}
