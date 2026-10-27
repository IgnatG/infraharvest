// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"log"

	"github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
	"github.com/IgnatG/infraharvest/internal/engine"
)

// EngineOptions returns the engine's options of what c knows about its
// provider's resource types, for roots that pin the provider release line
// of v: the arguments to leave out, their defaults and those kept only in
// state, the empty values that are settings of their own, the provider's
// built-in schema metadata, and the data sources, ARNs, placeholders and
// lifecycle settings of each type. The rest of the options are the
// caller's.
func (c *Catalogue) EngineOptions(v *version.Version) engine.Options {
	opts := engine.Options{Omit: c.OmitFor(v), Defaults: c.DefaultsFor(v), StateOnly: c.StateOnlyFor(v), KeepEmpty: c.KeepEmpty()}
	// Without the metadata, the engine finds defaults by planning, as it
	// always could: a broken file costs plans, not the import.
	if meta, err := schemameta.Builtin(c.Provider); err != nil {
		log.Printf("%s: the built-in schema metadata: %v", c.Provider, err)
	} else {
		opts.SchemaMetadata = meta
	}
	if sources := c.DataSources(); len(sources) > 0 {
		opts.DataSources = map[string]engine.DataSource{}
		for typ, d := range sources {
			opts.DataSources[typ] = engine.DataSource{Type: d.Type, Argument: d.Argument}
		}
	}
	for _, typ := range c.Types() {
		if e, ok := c.Entry(typ); ok && e.ARN != nil {
			if opts.ARNs == nil {
				opts.ARNs = map[string]engine.ARN{}
			}
			opts.ARNs[typ] = engine.ARN{Service: e.ARN.Service, ResourceType: e.ARN.ResourceType}
		}
	}
	if placeholders := c.Placeholders(); len(placeholders) > 0 {
		opts.Placeholders = map[string]engine.Placeholder{}
		for typ, p := range placeholders {
			opts.Placeholders[typ] = engine.Placeholder{OneOf: p.OneOf, Set: p.Set, Ignore: p.Ignore, Reason: p.Reason}
		}
	}
	if lifecycles := c.Lifecycles(); len(lifecycles) > 0 {
		opts.Lifecycles = map[string]engine.Lifecycle{}
		for typ, l := range lifecycles {
			opts.Lifecycles[typ] = engine.Lifecycle{CreateBeforeDestroy: l.CreateBeforeDestroy, Reason: l.Reason}
		}
	}
	return opts
}
