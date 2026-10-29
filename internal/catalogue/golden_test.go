// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// update rewrites the golden files from what the catalogue does now:
//
//	go test ./internal/catalogue -run TestGolden -update
//
// or, without a local toolchain, the remote workflow's golden task.
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// goldenListing is a golden test's input, testdata/<provider>/<type>.json:
// resources of the type as a lister records them.
type goldenListing struct {
	Resources []struct {
		ID         string            `json:"id"`
		Name       string            `json:"name"`
		Attributes map[string]string `json:"attributes,omitempty"`
	} `json:"resources"`
}

// alwaysGolden are the AWS and Google network, DNS, identity and key types,
// which each have a golden test whatever their entries hold (docs/catalogue.md
// lists them).
var alwaysGolden = []string{
	"aws_vpc", "aws_subnet", "aws_internet_gateway", "aws_nat_gateway",
	"aws_route_table", "aws_route_table_association", "aws_main_route_table_association",
	"aws_vpc_endpoint", "aws_security_group", "aws_security_group_rule", "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule",
	"aws_iam_role", "aws_iam_policy", "aws_iam_instance_profile", "aws_iam_role_policy", "aws_iam_role_policy_attachment",
	"aws_route53_zone", "aws_route53_record", "aws_kms_key", "aws_kms_alias",
	"google_compute_network", "google_compute_subnetwork", "google_compute_firewall", "google_compute_route",
	"google_dns_managed_zone", "google_service_account", "google_project_iam_custom_role",
	"google_kms_key_ring", "google_kms_crypto_key",
}

// Each catalogue entry that changes what an import does, and each
// alwaysGolden type, has a golden test: the resources of testdata/<provider>/<type>.json, as
// listed, give the import blocks, skips, data sources, children and
// omitted arguments of testdata/<provider>/<type>.golden.tf. A
// <type>.generated.tf, as -generate-config-out wrote it, goes through the
// arguments the catalogue omits too.
func TestGolden(t *testing.T) {
	providers, err := catalogue.Providers()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		c := catalogue.MustBuiltin(provider)
		for _, typ := range c.Types() {
			input := filepath.Join("testdata", provider, typ+".json")
			if _, err := os.Stat(input); err != nil {
				if needsGolden(c, typ) {
					t.Errorf("%s: %s has no golden test: add %s", c.Source(typ), typ, input)
				}
				continue
			}
			t.Run(typ, func(t *testing.T) {
				got := renderGolden(t, c, typ, input)
				golden := filepath.Join("testdata", provider, typ+".golden.tf")
				if *update {
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v: run go test ./internal/catalogue -run TestGolden -update", err)
				}
				if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
					t.Errorf("%s differs; got:\n%s", golden, got)
				}
			})
		}
	}
}

// Every golden input is for a catalogued type.
func TestGoldenInputsHaveEntries(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		provider := filepath.Base(filepath.Dir(input))
		typ := strings.TrimSuffix(filepath.Base(input), ".json")
		if _, ok := catalogue.MustBuiltin(provider).Entry(typ); !ok {
			t.Errorf("%s: %s isn't in the %s catalogue", input, typ, provider)
		}
	}
}

// needsGolden reports whether an entry changes what an import does, or its
// type is one of alwaysGolden.
func needsGolden(c *catalogue.Catalogue, typ string) bool {
	e, _ := c.Entry(typ)
	return len(e.ImportID) > 0 || e.NotImportable != "" || len(e.Skip) > 0 || len(e.Children) > 0 ||
		e.DataSource != nil || len(e.Omit) > 0 || len(e.Defaults) > 0 || len(e.StateOnly) > 0 || len(e.ProviderVersions) > 0 || len(e.KeepEmpty) > 0 || e.Placeholder != nil || e.Lifecycle != nil || slices.Contains(alwaysGolden, typ)
}

func renderGolden(t *testing.T, c *catalogue.Catalogue, typ, input string) []byte {
	t.Helper()
	content, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var listing goldenListing
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&listing); err != nil {
		t.Fatalf("%s: %v", input, err)
	}
	if len(listing.Resources) == 0 {
		t.Fatalf("%s: no resources", input)
	}
	e, _ := c.Entry(typ)
	var out bytes.Buffer
	fmt.Fprintf(&out, "# %s, from %s\n", typ, c.Source(typ))

	var imports []rootfile.Import
	var notes []string
	// resourceOmit has, by address, what the catalogue leaves out of a
	// resource's configuration depending on what its lister recorded.
	resourceOmit := map[string][]string{}
	var resourceNotes []string
	data := hclwrite.NewEmptyFile()
	for _, r := range listing.Resources {
		if !hclsyntax.ValidIdentifier(r.Name) {
			t.Fatalf("%s: name %q: use a valid Terraform name", input, r.Name)
		}
		id, ok, reason := c.ImportID(typ, r.ID, r.Attributes)
		switch skipped := c.Skipped(typ, r.ID, r.Attributes); {
		case !ok:
			notes = append(notes, fmt.Sprintf("%s: not importable: %s", r.ID, reason))
		case skipped != "":
			notes = append(notes, fmt.Sprintf("%s: left out by default, would import as %q: %s", r.ID, id, skipped))
		default:
			imports = append(imports, rootfile.Import{Type: typ, Name: r.Name, ID: id})
		}
		if extra := c.ResourceOmit(typ, r.Attributes); len(extra) > 0 {
			resourceOmit[typ+"."+r.Name] = extra
			resourceNotes = append(resourceNotes, fmt.Sprintf("%s: also left out, from what its lister recorded: %s", r.ID, strings.Join(extra, ", ")))
		}
		if ok && e.DataSource != nil {
			if len(data.Body().Blocks()) > 0 {
				data.Body().AppendNewline()
			}
			block := data.Body().AppendNewBlock("data", []string{e.DataSource.Type, r.Name})
			block.Body().SetAttributeValue(e.DataSource.Argument, cty.StringVal(id))
		}
	}
	if len(imports) > 0 {
		blocks, err := rootfile.ImportsFile(imports)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString("\n")
		out.Write(blocks)
	}
	if len(notes) > 0 {
		out.WriteString("\n")
		for _, note := range notes {
			fmt.Fprintf(&out, "# %s\n", note)
		}
	}
	if len(data.Body().Blocks()) > 0 {
		out.WriteString("\n# How configuration refers to a resource the selection leaves out:\n")
		out.Write(hclwrite.Format(data.Bytes()))
	}
	if len(e.Children) > 0 {
		out.WriteString("\n# Imported with each resource, where it has them, with its import ID:\n")
		for _, child := range e.Children {
			fmt.Fprintf(&out, "#   %s\n", child)
		}
	}
	omit := append(append([]string(nil), c.Omit(nil)[catalogue.AllTypes]...), e.Omit...)
	if len(e.Omit) > 0 {
		fmt.Fprintf(&out, "\n# Left out of generated configuration: %s\n", strings.Join(omit, ", "))
	}
	if len(resourceNotes) > 0 {
		out.WriteString("\n")
		for _, note := range resourceNotes {
			fmt.Fprintf(&out, "# %s\n", note)
		}
	}
	if len(e.Defaults) > 0 {
		sorted := append([]string(nil), e.Defaults...)
		sort.Strings(sorted)
		fmt.Fprintf(&out, "\n# Left out where the plan stays the same, as the default: %s\n", strings.Join(sorted, ", "))
	}
	if stateOnly := c.StateOnly(nil)[typ]; len(stateOnly) > 0 {
		sorted := append([]string(nil), stateOnly...)
		sort.Strings(sorted)
		fmt.Fprintf(&out, "\n# Kept in state only, so a plan may set: %s\n", strings.Join(sorted, ", "))
	}
	if len(e.KeepEmpty) > 0 {
		fmt.Fprintf(&out, "\n# Kept where set empty: %s\n", strings.Join(e.KeepEmpty, ", "))
	}
	for _, constraint := range slices.Sorted(maps.Keys(e.ProviderVersions)) {
		x := e.ProviderVersions[constraint]
		fmt.Fprintf(&out, "\n# With provider versions %s:\n", constraint)
		if len(x.Omit) > 0 {
			fmt.Fprintf(&out, "#   left out of generated configuration: %s\n", strings.Join(x.Omit, ", "))
		}
		if len(x.Defaults) > 0 {
			fmt.Fprintf(&out, "#   left out where the plan stays the same, as the default: %s\n", strings.Join(x.Defaults, ", "))
		}
		if len(x.StateOnly) > 0 {
			fmt.Fprintf(&out, "#   kept in state only: %s\n", strings.Join(x.StateOnly, ", "))
		}
	}
	if p := e.Placeholder; p != nil {
		fmt.Fprintf(&out, "\n# Without any of %s: placeholders, ignored by Terraform (%s):\n", strings.Join(p.OneOf, ", "), p.Reason)
		for _, arg := range slices.Sorted(maps.Keys(p.Set)) {
			fmt.Fprintf(&out, "#   %s = %q\n", arg, p.Set[arg])
		}
		fmt.Fprintf(&out, "#   lifecycle.ignore_changes = [%s]\n", strings.Join(p.Ignore, ", "))
	}
	if l := e.Lifecycle; l != nil {
		fmt.Fprintf(&out, "\n# Every resource gets lifecycle.create_before_destroy = %t (%s)\n", l.CreateBeforeDestroy, l.Reason)
	}
	generated := strings.TrimSuffix(input, ".json") + ".generated.tf"
	if _, err := os.Stat(generated); err == nil {
		out.WriteString("\n# Generated configuration, without the arguments the catalogue omits:\n")
		omit := c.Omit(nil)
		for address, extra := range resourceOmit {
			omit[address] = extra
		}
		out.Write(omitted(t, generated, omit))
	}
	return out.Bytes()
}

// omitted returns the generated configuration in path without the
// arguments omit names, as the engine leaves them out.
func omitted(t *testing.T, path string, omit map[string][]string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := hclfile.Parse(path, content)
	if err != nil {
		t.Fatal(err)
	}
	result := content
	if engine.OmitArguments(f, omit) {
		// As the engine saves it.
		result = bytes.TrimLeft(hclwrite.Format(f.Edit().Bytes()), "\n")
	}
	return bytes.ReplaceAll(result, []byte("\r\n"), []byte("\n"))
}
