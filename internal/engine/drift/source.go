// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// How a root's code sets an attribute that changed in the cloud
// (codeSource.Kind).
const (
	// sourceLiteral: a literal, or a key of an object or map literal.
	sourceLiteral = "literal"
	// sourceAbsent: no argument, as for a default or an empty value.
	sourceAbsent = "absent"
	// sourceLocal: a local value only this resource uses.
	sourceLocal = "local"
	// sourceSharedLocal: a local value other resources use too.
	sourceSharedLocal = "shared_local"
	// sourceReference: a reference to another resource or a data source.
	sourceReference = "reference"
	// sourceVariable: a variable, such as one whose default is another
	// root's value.
	sourceVariable = "variable"
	// sourceSensitiveVariable: a sensitive variable.
	sourceSensitiveVariable = "sensitive_variable"
	// sourceExpression: a function, a conditional, a for expression, a
	// dynamic block, or anything else that computes the value.
	sourceExpression = "expression"
	// sourceSharedTags: tags or labels several resources get from one
	// place: the provider's default tags, or a local their tags refer to.
	sourceSharedTags = "shared_tags"
	// sourceModule: the resource is in a module call, and what sets it
	// there can't be traced back to the call's arguments, or takes a
	// hand edit in the module.
	sourceModule = "module"
	// sourceResource: the whole resource, such as one deleted in the
	// cloud.
	sourceResource = "resource"
	// sourceNoBlock: the code has no block for the resource.
	sourceNoBlock = "no_block"
)

// codeSource is where a root's code sets an attribute that changed in the
// cloud, and how the code would take the cloud's value.
type codeSource struct {
	// File is the file, relative to the root; "" when there is none, as
	// for a resource in a module call.
	File string
	// Range is the expression that sets the attribute, or the block an
	// argument the code leaves out goes in.
	Range hcl.Range
	Kind  string
	// Edit takes the cloud's value into the code; nil when that is a hand
	// edit, as Reason says, or a secret.
	Edit *hclfile.Edit
	// Also are the other edits Edit takes, made with it, or not at all:
	// for a literal of a generated module made a variable of it, the
	// module's and the call's (see promoteLiteral).
	Also []hclfile.Edit
	// Reason says why there is no Edit.
	Reason string
	// instance says Edit is of the instance's own value, its key's in the
	// map the block's for_each is over (see eachResolver).
	instance bool
	// Secret says the value is a secret, which is never written.
	Secret bool
	// Module says, for a resource in a module call, how the module gets
	// the value from the argument of the call the edit is in, such as
	// ../../modules/bucket_0a1b2c3d/main.tf:4 var.tags.
	Module string
	// Note says what the report should say of an edit, such as a call's
	// argument set to null, whose module's default isn't null.
	Note string
}

// refreshedResource is a resource as the refresh found it: the state's
// values, the cloud's, and which of the cloud's are sensitive.
type refreshedResource struct {
	before, after, afterSensitive any
}

// driftEditor finds where the code sets what changed in the cloud, and
// makes the edits that take the cloud's values.
type driftEditor struct {
	resources map[string]refreshedResource
	// schemas type the values written; nil writes them as their JSON has
	// them.
	schemas *tfjson.ProviderSchemas
	// isDefault is the engine's (see Rules.IsDefault).
	isDefault func(typ, path string, expr hclsyntax.Expression) bool
	// refer refers to the managed resources by their IDs, ARNs and other
	// identifiers (see Rules.Referrer); nil to none.
	refer Referrer
	// sensitive are the values the plans mark sensitive, and forms the
	// forms text quotes them in: no edit may write one.
	sensitive, forms []string
	// secretForms are forms and the secrets a report hides among the
	// plans' other values (see driftSecretForms): no reason may quote one.
	secretForms []string
	// providerTagged are, by a resource's address and the path of a key
	// of its tags, why the resource can't leave out a key the provider's
	// default tags give it.
	providerTagged map[string]string
	// modules are the modules the root calls, by their keys in
	// modules.json (see loadDriftModules), to trace changes of their
	// resources to the calls.
	modules map[string]*driftModule
	// unreadModules are why each module the root calls that
	// loadDriftModules couldn't read can't be, by key, and by "" why
	// none can be.
	unreadModules map[string]string
	// adapters are the run's (see Options.Adapters), whose Inverse
	// traces a change of a curated module's resource to its call (see
	// curatedAdapter).
	adapters []adapters.Adapter
	// env is, for a root environments share, the environment checked:
	// its code is every environment's, so only env's var file, envVars
	// (see hclfile.EnvVarsPath), is edited, for a value a variable sets, or a
	// block only env has (see ownBlock).
	env, envVars string
	// ownFlags are, for a root environments share, the bool variables
	// only env sets true (see envOnlyFlags).
	ownFlags map[string]bool
	// current are the values the plan of the code has each resource of
	// the root module at, by address, which the refresh made the cloud's;
	// nil for one it creates.
	current map[string]any
}

func newDriftEditor(refresh, code *tfjson.Plan, rules Rules) *driftEditor {
	e := &driftEditor{resources: map[string]refreshedResource{}, isDefault: rules.IsDefault, providerTagged: map[string]string{}, current: map[string]any{}}
	if code != nil {
		for _, rc := range code.ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && rc.ModuleAddress == "" && rc.Change != nil {
				e.current[rc.Address] = rc.Change.Before
			}
		}
	}
	if refresh != nil {
		for _, rc := range refresh.ResourceDrift {
			if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
				e.resources[rc.Address] = refreshedResource{before: rc.Change.Before, after: rc.Change.After, afterSensitive: rc.Change.AfterSensitive}
			}
		}
	}
	var state *tfjson.State
	switch {
	case code != nil && code.PriorState != nil:
		state = code.PriorState
	case refresh != nil:
		state = refresh.PriorState
	}
	if rules.Referrer != nil {
		e.refer = rules.Referrer(rootStateValues(state))
	}
	seen := map[string]bool{}
	forEachChange([]*tfjson.Plan{refresh, code}, func(c *tfjson.Change) {
		secret.CollectSensitive(c.Before, c.BeforeSensitive, seen)
		secret.CollectSensitive(c.After, c.AfterSensitive, seen)
	})
	e.sensitive = slices.Sorted(maps.Keys(seen))
	e.forms = secret.TextForms(e.sensitive)
	e.secretForms = driftSecretForms(nil, refresh, code)
	return e
}

// rootStateValues returns the attribute values of the managed resources
// of the root module of state, by address.
func rootStateValues(state *tfjson.State) map[string]map[string]any {
	values := map[string]map[string]any{}
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return values
	}
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == tfjson.ManagedResourceMode {
			values[r.Address] = r.AttributeValues
		}
	}
	return values
}
