// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// providerSchemasEnv names the directory with the providers' schemas, as
// terraform providers schema -json writes them (schemas.json), and the
// versions they are of, as terraform version -json writes them
// (versions.json). The test workflow's catalogue-schemas job sets it.
const providerSchemasEnv = "INFRAHARVEST_PROVIDER_SCHEMAS"

// Every type, argument and nested block the built-in catalogue names is in
// the schema of the provider's newest release, the one generated roots
// pin, and every default converts to its attribute's type: a typo or a
// rename in the provider would otherwise leave an omit or a default doing
// nothing.
func TestCatalogueMatchesProviderSchemas(t *testing.T) {
	t.Parallel()
	dir := os.Getenv(providerSchemasEnv)
	if dir == "" {
		t.Skipf("%s isn't set: the test workflow's catalogue-schemas job sets it", providerSchemasEnv)
	}
	var schemas tfjson.ProviderSchemas
	readJSONFile(t, filepath.Join(dir, "schemas.json"), &schemas)
	var versions tfjson.VersionOutput
	readJSONFile(t, filepath.Join(dir, "versions.json"), &versions)
	providers, err := Providers()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			address := "registry.terraform.io/" + schemaSource(provider)
			schema, ok := schemas.Schemas[address]
			if !ok {
				t.Fatalf("no schema for %s in %s", address, dir)
			}
			v, err := version.NewVersion(versions.ProviderSelections[address])
			if err != nil {
				t.Fatalf("the version of %s: %v", address, err)
			}
			t.Logf("%s %s", address, v)
			c, err := Builtin(provider)
			if err != nil {
				t.Fatal(err)
			}
			for _, problem := range schemaProblems(c, schema, v) {
				t.Error(problem)
			}
		})
	}
}

// schemaSource is the source of the provider a built-in catalogue is for,
// as the schemas name it, in lower case: hashicorp/<provider>, but for
// azapi, which Microsoft publishes.
func schemaSource(provider string) string {
	if provider == "azapi" {
		return "azure/azapi"
	}
	return "hashicorp/" + provider
}

// readJSONFile decodes the JSON file at name into v.
func readJSONFile(t *testing.T, name string, v any) {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// schemaProblems returns what c names that schema, version v of c's
// provider, doesn't have. The provider_versions lists count only where
// their constraint accepts v. The entry for every type must name what at
// least one of the provider's types has.
func schemaProblems(c *Catalogue, schema *tfjson.ProviderSchema, v *version.Version) []string {
	var problems []string
	for _, typ := range slices.Sorted(maps.Keys(c.entries)) {
		e := c.entries[typ]
		fail := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("%s: %s: %s", c.sources[typ], typ, fmt.Sprintf(format, args...)))
		}
		var blocks []*tfjson.SchemaBlock
		if typ == AllTypes {
			for _, name := range slices.Sorted(maps.Keys(schema.ResourceSchemas)) {
				if s := schema.ResourceSchemas[name]; s != nil && s.Block != nil {
					blocks = append(blocks, s.Block)
				}
			}
		} else {
			s, ok := schema.ResourceSchemas[typ]
			if !ok || s == nil || s.Block == nil {
				fail("not a resource type of the provider")
				continue
			}
			blocks = []*tfjson.SchemaBlock{s.Block}
		}
		paths := map[string][]string{}
		add := func(list string, entries ...string) { paths[list] = append(paths[list], entries...) }
		var defaults []string
		for _, entry := range e.Omit {
			target, conditions, _ := strings.Cut(entry, " when ")
			name, _, _ := strings.Cut(target, "=")
			add("omit", name)
			for _, condition := range strings.Split(conditions, " and ") {
				arg, _, _ := strings.Cut(strings.TrimPrefix(condition, "!"), "=")
				if arg != "" && !strings.HasPrefix(arg, "@") {
					add("omit condition", arg)
				}
			}
		}
		add("state_only", e.StateOnly...)
		add("keep_empty", e.KeepEmpty...)
		defaults = append(defaults, e.Defaults...)
		if p := e.Placeholder; p != nil {
			add("placeholder.one_of", p.OneOf...)
			add("placeholder.ignore", p.Ignore...)
			add("placeholder.set", slices.Sorted(maps.Keys(p.Set))...)
		}
		for _, constraint := range slices.Sorted(maps.Keys(e.ProviderVersions)) {
			x := e.ProviderVersions[constraint]
			if x == nil || !accepts(constraint, v) {
				continue
			}
			for _, entry := range x.Omit {
				target, _, _ := strings.Cut(entry, " when ")
				name, _, _ := strings.Cut(target, "=")
				add("provider_versions: "+constraint+": omit", name)
			}
			add("provider_versions: "+constraint+": state_only", x.StateOnly...)
			defaults = append(defaults, x.Defaults...)
		}
		for _, list := range slices.Sorted(maps.Keys(paths)) {
			for _, path := range paths[list] {
				if !slices.ContainsFunc(blocks, func(b *tfjson.SchemaBlock) bool { return schemaHas(b, strings.Split(path, ".")) }) {
					fail("%s: %s isn't an argument or a nested block of the provider's schema", list, path)
				}
			}
		}
		for _, entry := range defaults {
			path, value, _ := strings.Cut(entry, "=")
			attr := schemaAttribute(blocks[0], strings.Split(path, "."))
			switch {
			case attr == nil:
				fail("defaults: %s isn't an attribute of the provider's schema", path)
			case !convertsTo(value, attr.AttributeType):
				fail("defaults: %s: %q isn't a %s", path, value, attr.AttributeType.FriendlyName())
			}
		}
		for _, child := range e.Children {
			if _, ok := schema.ResourceSchemas[child]; !ok {
				fail("children: %s isn't a resource type of the provider", child)
			}
		}
		if d := e.DataSource; d != nil {
			s, ok := schema.DataSourceSchemas[d.Type]
			switch {
			case !ok || s == nil || s.Block == nil:
				fail("data_source: %s isn't a data source of the provider", d.Type)
			case s.Block.Attributes[d.Argument] == nil:
				fail("data_source: %s has no argument %s", d.Type, d.Argument)
			}
		}
	}
	return problems
}

// schemaHas reports whether path, such as root_block_device.iops, names an
// attribute or a nested block in block, where * in path stands for a
// nested block of any type, as in an omit entry.
func schemaHas(block *tfjson.SchemaBlock, path []string) bool {
	if block == nil || len(path) == 0 {
		return false
	}
	name := path[0]
	if len(path) == 1 {
		_, isAttribute := block.Attributes[name]
		_, isBlock := block.NestedBlocks[name]
		return isAttribute || isBlock
	}
	for typ, nested := range block.NestedBlocks {
		if (name == "*" || typ == name) && nested != nil && schemaHas(nested.Block, path[1:]) {
			return true
		}
	}
	return false
}

// schemaAttribute returns the attribute at path in block, through its
// nested blocks, or nil if there is none.
func schemaAttribute(block *tfjson.SchemaBlock, path []string) *tfjson.SchemaAttribute {
	for ; block != nil && len(path) > 1; path = path[1:] {
		nested := block.NestedBlocks[path[0]]
		if nested == nil {
			return nil
		}
		block = nested.Block
	}
	if block == nil || len(path) != 1 {
		return nil
	}
	return block.Attributes[path[0]]
}

// convertsTo reports whether a default's value, as the catalogue writes
// it, is one of ty's values: a string, a number or a bool converts from
// its text. Values of other types, and patterns, aren't checked.
func convertsTo(value string, ty cty.Type) bool {
	if !ty.IsPrimitiveType() || strings.ContainsAny(value, "{[") {
		return true
	}
	_, err := convert.Convert(cty.StringVal(value), ty)
	return err == nil
}

// The check reports unknown types, arguments, nested blocks, children and
// data sources, and defaults of the wrong type, and accepts what the
// schema has.
func TestSchemaProblems(t *testing.T) {
	t.Parallel()
	schema := &tfjson.ProviderSchema{
		ResourceSchemas: map[string]*tfjson.Schema{
			"aws_thing": {Block: &tfjson.SchemaBlock{
				Attributes: map[string]*tfjson.SchemaAttribute{
					"name":     {AttributeType: cty.String},
					"size":     {AttributeType: cty.Number},
					"enabled":  {AttributeType: cty.Bool},
					"tags":     {AttributeType: cty.Map(cty.String)},
					"tags_all": {AttributeType: cty.Map(cty.String)},
				},
				NestedBlocks: map[string]*tfjson.SchemaBlockType{
					"timeouts": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"create": {AttributeType: cty.String}}}},
					"disk": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
						"iops":     {AttributeType: cty.Number},
						"tags_all": {AttributeType: cty.Map(cty.String)},
					}}},
				},
			}},
			"aws_part": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"id": {AttributeType: cty.String}}}},
		},
		DataSourceSchemas: map[string]*tfjson.Schema{
			"aws_thing": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"name": {AttributeType: cty.String}}}},
		},
	}
	v := version.Must(version.NewVersion("6.1.0"))
	catalogue := func(entries map[string]*Entry) *Catalogue {
		sources := map[string]string{}
		for typ := range entries {
			sources[typ] = "data/aws/test.yaml"
		}
		return &Catalogue{Provider: "aws", entries: entries, sources: sources}
	}

	good := catalogue(map[string]*Entry{
		AllTypes: {Omit: []string{"tags_all", "*.tags_all"}},
		"aws_thing": {
			Omit:       []string{"timeouts", "disk.iops=0 when enabled=false and @listed", "size"},
			Defaults:   []string{"size=10", "enabled=true", "disk.iops=3000", "name={name}", "tags={}"},
			StateOnly:  []string{"enabled"},
			KeepEmpty:  []string{"tags"},
			Children:   []string{"aws_part"},
			DataSource: &DataSource{Type: "aws_thing", Argument: "name"},
			ProviderVersions: map[string]*Versioned{
				// For older versions only: not checked against 6.1.0.
				"< 6.0":  {Omit: []string{"gone"}},
				">= 6.0": {Omit: []string{"disk.tags_all"}},
			},
		},
		"aws_part": {},
	})
	if problems := schemaProblems(good, schema, v); len(problems) > 0 {
		t.Errorf("a catalogue that matches the schema: got\n%s", strings.Join(problems, "\n"))
	}

	bad := catalogue(map[string]*Entry{
		AllTypes: {Omit: []string{"*.nowhere"}},
		"aws_thing": {
			Omit:       []string{"nme", "disk.iopz", "size when disk.missing"},
			Defaults:   []string{"size=ten", "enabled=yes", "timeouts", "disk.speed=1"},
			KeepEmpty:  []string{"tag"},
			Children:   []string{"aws_other"},
			DataSource: &DataSource{Type: "aws_thing", Argument: "id"},
			ProviderVersions: map[string]*Versioned{
				">= 6.0": {StateOnly: []string{"renamed"}},
			},
		},
		"aws_renamed": {},
		"aws_part":    {DataSource: &DataSource{Type: "aws_part", Argument: "id"}},
	})
	want := []string{
		"data/aws/test.yaml: *: omit: *.nowhere isn't an argument or a nested block of the provider's schema",
		"data/aws/test.yaml: aws_part: data_source: aws_part isn't a data source of the provider",
		"data/aws/test.yaml: aws_renamed: not a resource type of the provider",
		"data/aws/test.yaml: aws_thing: keep_empty: tag isn't an argument or a nested block of the provider's schema",
		"data/aws/test.yaml: aws_thing: omit: nme isn't an argument or a nested block of the provider's schema",
		"data/aws/test.yaml: aws_thing: omit: disk.iopz isn't an argument or a nested block of the provider's schema",
		"data/aws/test.yaml: aws_thing: omit condition: disk.missing isn't an argument or a nested block of the provider's schema",
		"data/aws/test.yaml: aws_thing: provider_versions: >= 6.0: state_only: renamed isn't an argument or a nested block of the provider's schema",
		`data/aws/test.yaml: aws_thing: defaults: size: "ten" isn't a number`,
		`data/aws/test.yaml: aws_thing: defaults: enabled: "yes" isn't a bool`,
		"data/aws/test.yaml: aws_thing: defaults: timeouts isn't an attribute of the provider's schema",
		"data/aws/test.yaml: aws_thing: defaults: disk.speed isn't an attribute of the provider's schema",
		"data/aws/test.yaml: aws_thing: children: aws_other isn't a resource type of the provider",
		"data/aws/test.yaml: aws_thing: data_source: aws_thing has no argument id",
	}
	if got := schemaProblems(bad, schema, v); !slices.Equal(got, want) {
		t.Errorf("problems: got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
