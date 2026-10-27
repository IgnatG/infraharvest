// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command adaptercheck checks the curated module adapters against module
// versions. Run from the repository root.
//
//	go run ./internal/adapters/cmd/adaptercheck                 # report newer module releases
//	go run ./internal/adapters/cmd/adaptercheck -notes notes.md # and note pinned releases not usable yet
//	go run ./internal/adapters/cmd/adaptercheck -write          # refresh the pinned snapshots
//
// The report, in Markdown, lists each adapter whose module has a newer
// release, whether the adapter fits that release's interface and the
// resources it maps onto, and whether the release accepts the newest
// major release of each provider the adapter uses; it is empty when every
// adapter pins the newest release. The notes list each adapter that pins
// its module's newest release when that release accepts only an older
// major release of a provider, which an import doesn't call: there is
// nothing to bump to, so the notes aren't part of the report. -write
// fetches the interface of each pinned version into
// internal/adapters/testdata/interfaces.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/adapters/moduleinterface"
	"github.com/IgnatG/infraharvest/internal/engine"
)

func main() {
	write := flag.Bool("write", false, "refresh the interface snapshots of the pinned versions")
	notesFile := flag.String("notes", "", "write the notes on pinned releases not usable yet to this file, not standard error")
	flag.Parse()
	if err := run(*write, *notesFile); err != nil {
		log.Fatal(err)
	}
}

func run(write bool, notesFile string) error {
	notes := io.Writer(os.Stderr)
	if notesFile != "" {
		f, err := os.Create(notesFile)
		if err != nil {
			return err
		}
		defer f.Close()
		notes = f
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &moduleinterface.Client{HTTP: &http.Client{Timeout: time.Minute}, Registry: moduleinterface.RegistryURL}

	var failed []string
	for _, command := range adapters.Providers() {
		for _, a := range adapters.For(command) {
			var err error
			if write {
				err = writeSnapshot(ctx, client, a)
			} else {
				err = check(ctx, client, a, notes)
			}
			// One module's registry or GitHub error mustn't hide what
			// the others found: report it and go on.
			if err != nil {
				failed = append(failed, a.Source)
				if !write {
					fmt.Printf("- `%s`: couldn't be checked: %v.\n", a.Source, err)
				}
				log.Printf("%s: %v", a.Source, err)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of the adapters' modules couldn't be read: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// check prints a line when the module an adapter pins has a newer
// release, saying whether the adapter fits that release's interface and
// resources, and whether the release accepts the newest major release of
// the providers the adapter uses: the root's (see moduleProvider), and the
// adapter's Providers. When the adapter pins the newest release, it writes a line
// to notes instead if that release doesn't.
func check(ctx context.Context, client *moduleinterface.Client, a adapters.Adapter, notes io.Writer) error {
	latest, err := client.Latest(ctx, a.Source)
	if err != nil {
		return err
	}
	providers := append([]string{moduleProvider(a)}, a.Providers...)
	if latest == a.Version {
		return checkPinned(ctx, client, a, providers, notes)
	}
	iface, err := client.Fetch(ctx, a.Source, latest)
	if err != nil {
		return err
	}
	held, err := moduleinterface.CheckProviders(iface, providers, newestRelease(ctx, client.HTTP))
	if err != nil {
		return err
	}
	line := fmt.Sprintf("- `%s`: the adapter pins %s, the newest release is %s.", a.Source, a.Version, latest)
	var changes []string
	// The pinned snapshot, if it records types, tells what changed.
	if pinned, err := readSnapshot(a); err == nil {
		changes = moduleinterface.TypeChanges(pinned, iface, a.Inputs)
	}
	problems := moduleinterface.Check(a, iface)
	problems = append(problems, moduleinterface.CheckResources(a.Targets, iface)...)
	if len(problems) > 0 {
		line += fmt.Sprintf(" The adapter doesn't fit it: %s.", strings.Join(problems, "; "))
	}
	if len(changes) > 0 {
		line += fmt.Sprintf(" Variables the adapter sets changed type: %s; check what its Map sets them to.", strings.Join(changes, "; "))
	}
	if len(held) > 0 {
		line += fmt.Sprintf(" The release isn't usable yet: %s; an import doesn't call a module that would hold the root a major release behind.", strings.Join(held, "; "))
	}
	switch {
	case len(problems) > 0 || len(changes) > 0:
	case len(held) > 0:
		line += " The adapter fits its interface's names, recorded defaults and resources: pin a release once one accepts the newest major releases."
	default:
		line += " The adapter fits its interface's names, recorded defaults and resources, and the release accepts the newest major release of each provider the adapter uses: pin it, refresh the snapshot with `go run ./internal/adapters/cmd/adaptercheck -write` (or the adapters workflow with write on the branch), check the adapter's Defaults against the new variables.tf, and let the e2e test confirm the plans."
	}
	fmt.Println(line)
	return nil
}

// checkPinned writes a line to notes when the release an adapter pins,
// the newest, accepts no release of the newest major release line of one
// of providers. The pinned snapshot has the release's providers, unless
// it was written before they were recorded: then the release is read.
func checkPinned(ctx context.Context, client *moduleinterface.Client, a adapters.Adapter, providers []string, notes io.Writer) error {
	iface, err := readSnapshot(a)
	if err != nil || iface.Providers == nil {
		if iface, err = client.Fetch(ctx, a.Source, a.Version); err != nil {
			return err
		}
	}
	held, err := moduleinterface.CheckProviders(iface, providers, newestRelease(ctx, client.HTTP))
	if err != nil {
		return err
	}
	if len(held) > 0 {
		_, err = fmt.Fprintf(notes, "- `%s`: the adapter pins %s, the newest release, which isn't usable yet: %s. An import leaves the resources it would take in the root until a release accepts the newest major releases.\n", a.Source, a.Version, strings.Join(held, "; "))
	}
	return err
}

// newestRelease looks providers' releases up in their registries, as the
// import does when it pins the root's provider: each registry answer is
// asked for once.
func newestRelease(ctx context.Context, client *http.Client) moduleinterface.NewestRelease {
	return func(source string, constraints []version.Constraints) (*version.Version, error) {
		return engine.ProviderVersion(ctx, client, "", source, constraints)
	}
}

// moduleProvider returns the Terraform provider a registry module's source
// ends in, such as azurerm: that of the roots that call it.
func moduleProvider(a adapters.Adapter) string {
	module, _ := moduleinterface.SplitSource(a.Source)
	parts := strings.Split(module, "/")
	return parts[len(parts)-1]
}

// snapshotPath is where the interface of the version an adapter pins is
// kept: internal/adapters/testdata/interfaces/<provider>/<module name>.json,
// or <submodule name>.json.
func snapshotPath(a adapters.Adapter) string {
	module, subdir := moduleinterface.SplitSource(a.Source)
	name := strings.Split(module, "/")[1]
	if subdir != "" {
		name = path.Base(subdir)
	}
	return filepath.Join("internal", "adapters", "testdata", "interfaces", moduleProvider(a), name+".json")
}

// readSnapshot reads the interface snapshot of the version an adapter pins.
func readSnapshot(a adapters.Adapter) (*moduleinterface.Interface, error) {
	content, err := os.ReadFile(snapshotPath(a))
	if err != nil {
		return nil, err
	}
	var iface moduleinterface.Interface
	if err := json.Unmarshal(content, &iface); err != nil {
		return nil, err
	}
	return &iface, nil
}

// writeSnapshot writes the interface of the version an adapter pins to its
// snapshotPath.
func writeSnapshot(ctx context.Context, client *moduleinterface.Client, a adapters.Adapter) error {
	iface, err := client.Fetch(ctx, a.Source, a.Version)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(iface, "", "  ")
	if err != nil {
		return err
	}
	file := snapshotPath(a)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	log.Printf("writing %s", file)
	return os.WriteFile(file, append(content, '\n'), 0o644)
}
