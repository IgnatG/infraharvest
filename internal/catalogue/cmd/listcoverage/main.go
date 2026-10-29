// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command listcoverage counts, for the newest release of each provider in
// providers, the resource types its schema has and the list resources:
// the types `terraform query` can discover. It names the list resources no
// infraharvest lister lists, so the gap between what Terraform can find on
// its own and what infraharvest finds shows over time. Run from the
// repository root, with terraform on the PATH:
//
//	go run ./internal/catalogue/cmd/listcoverage                          # the report, in Markdown
//	go run ./internal/catalogue/cmd/listcoverage -json list-coverage.json # and the counts as JSON
//
// The types infraharvest lists are those docs/reference/services.md names:
// the page is generated from the listers and the catalogue, and a golden
// test keeps it current, so it is the one list of them.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// providers are the providers whose newest releases are counted. awscc has
// no listers of its own: it is there to compare with aws.
var providers = []string{"hashicorp/aws", "hashicorp/awscc", "hashicorp/azurerm", "hashicorp/google"}

// registry is the address Terraform gives a provider of the public
// registry, in the schema's keys and the version's provider selections.
const registry = "registry.terraform.io/"

func main() {
	services := flag.String("services", filepath.Join("docs", "reference", "services.md"), "the services reference, which names the types infraharvest lists")
	jsonFile := flag.String("json", "", "also write the counts to this file, as JSON")
	flag.Parse()
	if err := run(*services, *jsonFile); err != nil {
		log.Fatal(err)
	}
}

func run(servicesFile, jsonFile string) error {
	page, err := os.ReadFile(servicesFile)
	if err != nil {
		return err
	}
	listed := listedTypes(page)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	schemas, versions, err := providerSchemas(ctx, providers)
	if err != nil {
		return err
	}
	var report []Coverage
	for _, source := range providers {
		s, ok := schemas[registry+source]
		if !ok {
			return fmt.Errorf("terraform's schema has no %s", source)
		}
		report = append(report, compare(source, versions[registry+source], s, listed))
	}
	if jsonFile != "" {
		content, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(jsonFile, append(content, '\n'), 0o644); err != nil {
			return err
		}
	}
	return writeMarkdown(os.Stdout, report)
}

// providerSchemas initialises a root of its own that requires each of
// sources at no version, so that Terraform picks the newest release, and
// returns the schemas Terraform reads from them, by provider address, and
// the versions it picked.
func providerSchemas(ctx context.Context, sources []string) (map[string]Schema, map[string]string, error) {
	dir, err := os.MkdirTemp("", "listcoverage-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	var root strings.Builder
	root.WriteString("terraform {\n  required_providers {\n")
	for _, source := range sources {
		fmt.Fprintf(&root, "    %s = { source = %q }\n", source[strings.LastIndex(source, "/")+1:], source)
	}
	root.WriteString("  }\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		return nil, nil, err
	}
	if _, err := terraform(ctx, dir, "init", "-input=false", "-no-color"); err != nil {
		return nil, nil, err
	}
	out, err := terraform(ctx, dir, "version", "-json")
	if err != nil {
		return nil, nil, err
	}
	var v struct {
		ProviderSelections map[string]string `json:"provider_selections"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, nil, fmt.Errorf("terraform version: %w", err)
	}
	out, err = terraform(ctx, dir, "providers", "schema", "-json")
	if err != nil {
		return nil, nil, err
	}
	schemas, err := parseSchemas(bytes.NewReader(out))
	if err != nil {
		return nil, nil, err
	}
	return schemas, v.ProviderSelections, nil
}

// terraform runs terraform in dir and returns what it writes to standard
// output; when it fails, the error carries what it wrote to standard error.
func terraform(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "terraform", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terraform %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Schema is what a provider's schema says of its types: the names of its
// resource types and of its list resources, sorted.
type Schema struct {
	Resources     []string
	ListResources []string
}

// parseSchemas reads the output of `terraform providers schema -json`.
// Only the names of the types are kept: the schemas themselves, tens of
// megabytes for aws, are left undecoded.
func parseSchemas(r io.Reader) (map[string]Schema, error) {
	var doc struct {
		ProviderSchemas map[string]struct {
			ResourceSchemas     map[string]json.RawMessage `json:"resource_schemas"`
			ListResourceSchemas map[string]json.RawMessage `json:"list_resource_schemas"`
		} `json:"provider_schemas"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("the providers' schema: %w", err)
	}
	schemas := map[string]Schema{}
	for address, p := range doc.ProviderSchemas {
		schemas[address] = Schema{
			Resources:     slices.Sorted(maps.Keys(p.ResourceSchemas)),
			ListResources: slices.Sorted(maps.Keys(p.ListResourceSchemas)),
		}
	}
	return schemas, nil
}

// listedTypes returns the Terraform types the services reference names:
// each type in backquotes in the last cell of a row of its tables, where
// the first names the service.
func listedTypes(page []byte) map[string]bool {
	listed := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(page))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		parts := strings.Split(cells[len(cells)-1], "`")
		// The odd parts are those between backquotes.
		for i := 1; i < len(parts); i += 2 {
			listed[parts[i]] = true
		}
	}
	return listed
}

// Coverage is what the report says of one provider release.
type Coverage struct {
	Provider      string `json:"provider"`
	Version       string `json:"version"`
	Resources     int    `json:"resources"`
	ListResources int    `json:"list_resources"`
	// Unlisted are the list resources no infraharvest lister lists,
	// sorted.
	Unlisted []string `json:"unlisted"`
}

// Percent is the share of the provider's resource types that are list
// resources.
func (c Coverage) Percent() float64 {
	if c.Resources == 0 {
		return 0
	}
	return 100 * float64(c.ListResources) / float64(c.Resources)
}

// compare counts a provider's types and finds the list resources not in
// listed.
func compare(provider, version string, s Schema, listed map[string]bool) Coverage {
	c := Coverage{Provider: provider, Version: version, Resources: len(s.Resources), ListResources: len(s.ListResources), Unlisted: []string{}}
	for _, typ := range s.ListResources {
		if !listed[typ] {
			c.Unlisted = append(c.Unlisted, typ)
		}
	}
	return c
}

// writeMarkdown writes the report: a table of the counts, then, for each
// provider with list resources infraharvest doesn't list, those types,
// collapsed.
func writeMarkdown(w io.Writer, report []Coverage) error {
	var b strings.Builder
	b.WriteString("## Terraform list resources\n\n")
	b.WriteString("The resource types `terraform query` can discover in the newest release of each provider, and those of them no infraharvest lister lists.\n\n")
	b.WriteString("| Provider | Version | Resources | List resources | % | Not listed by infraharvest |\n|---|---|---:|---:|---:|---:|\n")
	for _, c := range report {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %.1f | %d |\n", c.Provider, c.Version, c.Resources, c.ListResources, c.Percent(), len(c.Unlisted))
	}
	for _, c := range report {
		if len(c.Unlisted) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n<details><summary><code>%s</code> %s: %d list resources not listed by infraharvest</summary>\n\n", c.Provider, c.Version, len(c.Unlisted))
		for _, typ := range c.Unlisted {
			fmt.Fprintf(&b, "- `%s`\n", typ)
		}
		b.WriteString("\n</details>\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
