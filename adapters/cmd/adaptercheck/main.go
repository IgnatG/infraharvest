// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command adaptercheck checks the curated module adapters against module
// versions. Run from the repository root.
//
//	go run ./adapters/cmd/adaptercheck          # report newer module releases
//	go run ./adapters/cmd/adaptercheck -write   # refresh the pinned snapshots
//
// The report, in Markdown, lists each adapter whose module has a newer
// release, and whether the adapter fits that release's interface; it is
// empty when every adapter pins the newest release. -write fetches the
// interface of each pinned version into adapters/testdata/interfaces.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/adapters"
	"github.com/IgnatG/infraharvest/adapters/moduleinterface"
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

	for _, provider := range adapters.Providers() {
		for _, a := range adapters.For(provider) {
			if write {
				if err := writeSnapshot(ctx, client, a); err != nil {
					return err
				}
				continue
			}
			latest, err := client.Latest(ctx, a.Source)
			if err != nil {
				return err
			}
			if latest == a.Version {
				continue
			}
			iface, err := client.Fetch(ctx, a.Source, latest)
			if err != nil {
				return err
			}
			fmt.Printf("- `%s`: the adapter pins %s, the newest release is %s.", a.Source, a.Version, latest)
			if problems := moduleinterface.Check(a, iface); len(problems) > 0 {
				fmt.Printf(" The adapter doesn't fit it: %s.\n", strings.Join(problems, "; "))
			} else {
				fmt.Printf(" The adapter fits its interface: pin it, refresh the snapshot with `go run ./adapters/cmd/adaptercheck -write`, and let the e2e test confirm the plans.\n")
			}
		}
	}
	return nil
}

// writeSnapshot writes the interface of the version an adapter pins to
// adapters/testdata/interfaces/<module name>.json.
func writeSnapshot(ctx context.Context, client *moduleinterface.Client, a adapters.Adapter) error {
	iface, err := client.Fetch(ctx, a.Source, a.Version)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(iface, "", "  ")
	if err != nil {
		return err
	}
	name := strings.Split(a.Source, "/")[1]
	path := filepath.Join("adapters", "testdata", "interfaces", name+".json")
	log.Printf("writing %s", path)
	return os.WriteFile(path, append(content, '\n'), 0o644)
}
