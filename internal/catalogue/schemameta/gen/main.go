// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

// Command gen writes what a Terraform provider's own code says about its
// resource types' arguments and its schema JSON leaves out: the defaults
// (Default) and the constraints between arguments (ExactlyOneOf,
// ConflictsWith, AtLeastOneOf, RequiredWith), as the JSON the schemameta
// package embeds.
//
// It is never built with infraharvest's module, which doesn't depend on
// any provider (hence the ignore constraint): the schema-metadata task of
// the remote workflow copies this file and the provider's own one
// (provider_<name>.go, which defines loadProvider) into a checkout of the
// provider at the release it reads, drops the constraint, and runs it
// there with the provider's go.mod, so that it can reach the provider's
// internal packages:
//
//	go run ./ihschemameta -version 6.70.0 -out aws.json
//
// The workflow then gzips the file, as the package embeds it.
//
// Plugin SDK v2 resources give everything. Plugin Framework resources give
// only their defaults: their constraints are validators, whose settings
// they don't expose, so they are marked framework and the engine falls
// back to what Terraform tells it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// file is what the JSON holds: the format schemameta.File reads.
type file struct {
	Provider  string                `json:"provider"`
	Version   string                `json:"version"`
	Resources map[string]*typeEntry `json:"resources"`
}

// typeEntry is one resource type's arguments that have metadata, by their
// path through nested blocks without indexes (bgp.asn).
type typeEntry struct {
	Framework bool                 `json:"framework,omitempty"`
	Arguments map[string]*argument `json:"arguments,omitempty"`
}

// argument is one argument's metadata. Default is a bool, a number or a
// string; the constraints name other arguments by their paths.
type argument struct {
	Default       any      `json:"default,omitempty"`
	Suppress      bool     `json:"suppress,omitempty"`
	ExactlyOneOf  []string `json:"exactly_one_of,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	AtLeastOneOf  []string `json:"at_least_one_of,omitempty"`
	RequiredWith  []string `json:"required_with,omitempty"`
}

func (a *argument) empty() bool {
	return a.Default == nil && len(a.ExactlyOneOf)+len(a.ConflictsWith)+len(a.AtLeastOneOf)+len(a.RequiredWith) == 0
}

func main() {
	release := flag.String("version", "", "the provider release the checkout is of, such as 6.70.0")
	out := flag.String("out", "", "the JSON file to write")
	flag.Parse()
	if *release == "" || *out == "" {
		log.Fatal("usage: gen -version <release> -out <file>")
	}
	ctx := context.Background()
	source, sdk, framework, err := loadProvider(ctx)
	if err != nil {
		log.Fatalf("load the provider: %v", err)
	}
	f := &file{Provider: source, Version: strings.TrimPrefix(*release, "v"), Resources: map[string]*typeEntry{}}
	for typ, r := range sdk.ResourcesMap {
		args := map[string]*argument{}
		sdkArguments(r, "", args)
		if len(args) > 0 {
			f.Resources[typ] = &typeEntry{Arguments: args}
		}
	}
	if framework != nil {
		if err := frameworkResources(ctx, framework, f.Resources); err != nil {
			log.Fatal(err)
		}
	}
	content, err := encode(f)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, content, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("%s %s: %d resource types with metadata", f.Provider, f.Version, len(f.Resources))
}

// sdkArguments records the arguments of r, a resource or a nested block
// at prefix, that have metadata, and those of its nested blocks.
// Arguments only the provider sets (computed and not optional) have none
// that configuration can use.
func sdkArguments(r *schema.Resource, prefix string, args map[string]*argument) {
	for name, s := range r.SchemaMap() {
		if s.Computed && !s.Optional {
			continue
		}
		path := prefix + name
		a := &argument{
			ExactlyOneOf:  withoutIndexes(s.ExactlyOneOf),
			ConflictsWith: withoutIndexes(s.ConflictsWith),
			AtLeastOneOf:  withoutIndexes(s.AtLeastOneOf),
			RequiredWith:  withoutIndexes(s.RequiredWith),
		}
		// Only a value the schema holds: a DefaultFunc reads the
		// environment, so its value is the machine's, not the provider's.
		if s.Default != nil {
			if v, ok := primitive(s.Default); ok {
				a.Default = v
				// A suppressed difference, such as of case, lets another
				// value plan like the default: only an equal one proves
				// anything then.
				a.Suppress = s.DiffSuppressFunc != nil
			}
		}
		if !a.empty() {
			args[path] = a
		}
		if nested, ok := s.Elem.(*schema.Resource); ok {
			sdkArguments(nested, path+".", args)
		}
	}
}

// withoutIndexes returns keys, such as network_interface.0.subnet_id,
// without their list indexes (network_interface.subnet_id), sorted, as
// configuration paths are.
func withoutIndexes(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	paths := make([]string, 0, len(keys))
	for _, key := range keys {
		var kept []string
		for _, segment := range strings.Split(key, ".") {
			if _, err := strconv.Atoi(segment); err != nil {
				kept = append(kept, segment)
			}
		}
		paths = append(paths, strings.Join(kept, "."))
	}
	sort.Strings(paths)
	return slices.Compact(paths)
}

// primitive returns v, a schema's Default, as a JSON bool, number or
// string. Providers use named types for some, such as an SDK's enum of
// strings, so it goes by kind.
func primitive(v any) (any, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), true
	case reflect.String:
		return rv.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return nil, false
}

// frameworkResources records p's resources, each marked framework, with
// the defaults of their arguments and those of their nested blocks.
// Nested attributes, whose values are objects, have no path a
// configuration argument has.
func frameworkResources(ctx context.Context, p provider.Provider, resources map[string]*typeEntry) error {
	var meta provider.MetadataResponse
	p.Metadata(ctx, provider.MetadataRequest{}, &meta)
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var name resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: meta.TypeName}, &name)
		var s resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &s)
		if s.Diagnostics.HasError() {
			return fmt.Errorf("%s: schema: %v", name.TypeName, s.Diagnostics)
		}
		args := map[string]*argument{}
		frameworkArguments(ctx, s.Schema.Attributes, s.Schema.Blocks, "", args)
		entry := &typeEntry{Framework: true}
		if len(args) > 0 {
			entry.Arguments = args
		}
		resources[name.TypeName] = entry
	}
	return nil
}

// frameworkArguments records the defaults of attrs at prefix, and of the
// attributes of blocks.
func frameworkArguments(ctx context.Context, attrs map[string]fwschema.Attribute, blocks map[string]fwschema.Block, prefix string, args map[string]*argument) {
	for name, attr := range attrs {
		if v, ok := frameworkDefault(ctx, attr); ok {
			args[prefix+name] = &argument{Default: v}
		}
	}
	for name, block := range blocks {
		switch b := block.(type) {
		case fwschema.ListNestedBlock:
			frameworkArguments(ctx, b.NestedObject.Attributes, b.NestedObject.Blocks, prefix+name+".", args)
		case fwschema.SetNestedBlock:
			frameworkArguments(ctx, b.NestedObject.Attributes, b.NestedObject.Blocks, prefix+name+".", args)
		case fwschema.SingleNestedBlock:
			frameworkArguments(ctx, b.Attributes, b.Blocks, prefix+name+".", args)
		}
	}
}

// frameworkDefault returns the static default of a primitive attribute,
// if it has one that is known and not null.
func frameworkDefault(ctx context.Context, attr fwschema.Attribute) (any, bool) {
	switch a := attr.(type) {
	case fwschema.BoolAttribute:
		if a.Default != nil {
			var r defaults.BoolResponse
			a.Default.DefaultBool(ctx, defaults.BoolRequest{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return r.PlanValue.ValueBool(), true
			}
		}
	case fwschema.StringAttribute:
		if a.Default != nil {
			var r defaults.StringResponse
			a.Default.DefaultString(ctx, defaults.StringRequest{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return r.PlanValue.ValueString(), true
			}
		}
	case fwschema.Int64Attribute:
		if a.Default != nil {
			var r defaults.Int64Response
			a.Default.DefaultInt64(ctx, defaults.Int64Request{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return r.PlanValue.ValueInt64(), true
			}
		}
	case fwschema.Int32Attribute:
		if a.Default != nil {
			var r defaults.Int32Response
			a.Default.DefaultInt32(ctx, defaults.Int32Request{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return int64(r.PlanValue.ValueInt32()), true
			}
		}
	case fwschema.Float64Attribute:
		if a.Default != nil {
			var r defaults.Float64Response
			a.Default.DefaultFloat64(ctx, defaults.Float64Request{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return r.PlanValue.ValueFloat64(), true
			}
		}
	case fwschema.Float32Attribute:
		if a.Default != nil {
			var r defaults.Float32Response
			a.Default.DefaultFloat32(ctx, defaults.Float32Request{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				return float64(r.PlanValue.ValueFloat32()), true
			}
		}
	case fwschema.NumberAttribute:
		if a.Default != nil {
			var r defaults.NumberResponse
			a.Default.DefaultNumber(ctx, defaults.NumberRequest{}, &r)
			if !r.Diagnostics.HasError() && !r.PlanValue.IsNull() && !r.PlanValue.IsUnknown() {
				if f, accuracy := r.PlanValue.ValueBigFloat().Float64(); accuracy == 0 {
					return f, true
				}
			}
		}
	}
	return nil, false
}

// encode writes f as compact JSON with one resource type to a line, in
// order, so that a new release's file differs from the last one's only
// in the lines of the types that changed.
func encode(f *file) ([]byte, error) {
	var b bytes.Buffer
	header, err := json.Marshal(struct {
		Provider string `json:"provider"`
		Version  string `json:"version"`
	}{f.Provider, f.Version})
	if err != nil {
		return nil, err
	}
	b.Write(header[:len(header)-1])
	b.WriteString(`,"resources":{`)
	types := make([]string, 0, len(f.Resources))
	for typ := range f.Resources {
		types = append(types, typ)
	}
	sort.Strings(types)
	for i, typ := range types {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
		key, err := json.Marshal(typ)
		if err != nil {
			return nil, err
		}
		// encoding/json writes map keys in order.
		value, err := json.Marshal(f.Resources[typ])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteString("\n}}\n")
	return b.Bytes(), nil
}
