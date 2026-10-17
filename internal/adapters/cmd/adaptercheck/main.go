// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command adaptercheck checks the curated module adapters against module
// versions. Run from the repository root.
//
//	go run ./internal/adapters/cmd/adaptercheck          # report newer module releases
//	go run ./internal/adapters/cmd/adaptercheck -write   # refresh the pinned snapshots
//
// The report, in Markdown, lists each adapter whose module has a newer
// release, and whether the adapter fits that release's interface; it is
// empty when every adapter pins the newest release. -write fetches the
// interface of each pinned version into internal/adapters/testdata/interfaces.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/adapters/moduleinterface"
)

func main() {
	write := flag.Bool("write", false, "refresh the interface snapshots of the pinned versions")
	flag.Parse()
	if err := run(*write); err != nil {
		log.Fatal(err)
	}
}

func run(write bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &moduleinterface.Client{HTTP: &http.Client{Timeout: time.Minute}, Registry: moduleinterface.RegistryURL}

	var failed []string
	for _, provider := range adapters.Providers() {
		for _, a := range adapters.For(provider) {
			var err error
			if write {
				err = writeSnapshot(ctx, client, a)
			} else {
				err = check(ctx, client, a)
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
// release, saying whether the adapter fits that release's interface.
func check(ctx context.Context, client *moduleinterface.Client, a adapters.Adapter) error {
	latest, err := client.Latest(ctx, a.Source)
	if err != nil {
		return err
	}
	if latest == a.Version {
		return nil
	}
	iface, err := client.Fetch(ctx, a.Source, latest)
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
	if len(problems) > 0 {
		line += fmt.Sprintf(" The adapter doesn't fit it: %s.", strings.Join(problems, "; "))
	}
	if len(changes) > 0 {
		line += fmt.Sprintf(" Variables the adapter sets changed type: %s; check what its Map sets them to.", strings.Join(changes, "; "))
	}
	if len(problems) == 0 && len(changes) == 0 {
		line += " The adapter fits its interface's names and recorded defaults: pin it, refresh the snapshot with `go run ./internal/adapters/cmd/adaptercheck -write` (or the adapters workflow with write on the branch), check the adapter's Defaults against the new variables.tf, and let the e2e test confirm the plans."
	}
	fmt.Println(line)
	return nil
}

// snapshotPath is where the interface of the version an adapter pins is
// kept: internal/adapters/testdata/interfaces/<provider>/<module name>.json,
// or <submodule name>.json.
func snapshotPath(a adapters.Adapter) string {
	module, subdir := moduleinterface.SplitSource(a.Source)
	parts := strings.Split(module, "/")
	name := parts[1]
	if subdir != "" {
		name = path.Base(subdir)
	}
	return filepath.Join("internal", "adapters", "testdata", "interfaces", parts[len(parts)-1], name+".json")
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
