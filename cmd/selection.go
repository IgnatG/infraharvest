// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/managed"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// DefaultSelectionFile is where discover writes the selection file unless
// --selection says otherwise.
const DefaultSelectionFile = "selection.yaml"

// checkSelectionOptions makes an import say what to import: a selection
// file, or --all. An import without either could bring a whole account
// under Terraform by accident.
func checkSelectionOptions(options ImportOptions) error {
	switch {
	case options.Discover:
		if options.All {
			return errors.New("discover lists everything; --all is for import")
		}
		return nil
	case options.Selection != "" && options.All:
		return errors.New("use either --selection or --all")
	case options.Selection == "" && !options.All:
		return selection.ErrNoSelection
	}
	return nil
}

// excludedByDefault asks the provider which listed resources it leaves out
// unless told otherwise, by "type lister-ID".
func excludedByDefault(ctx context.Context, provider terraformutils.ProviderGenerator, listed map[string][]terraformutils.Resource) (map[string]string, error) {
	withDefaults, ok := provider.(terraformutils.ProviderWithSelectionDefaults)
	if !ok {
		return nil, nil
	}
	var all []terraformutils.Resource
	for _, resources := range listed {
		all = append(all, resources...)
	}
	return withDefaults.ExcludedByDefault(ctx, all)
}

// selectionFile loads the selection file once per run; nil for --all.
func (r *engineRun) selectionFile(path string) (*selection.File, error) {
	if path == "" {
		return nil, nil
	}
	if r.selection == nil {
		f, err := selection.Load(path)
		if err != nil {
			return nil, err
		}
		r.selection = f
	}
	return r.selection, nil
}

// selectResources keeps the listed resources to import and records the
// others as excluded. A resource the selection file lists follows it; one
// it doesn't list follows the provider's defaults, then the file's rules
// and defaults. With no file (--all), the provider's defaults decide.
// It also returns the resources it leaves out.
func (r *engineRun) selectResources(listed map[string][]terraformutils.Resource, defaults map[string]string, f *selection.File, importID func(terraformutils.Resource) (string, bool)) (map[string][]terraformutils.Resource, []engine.External) {
	selected := make(map[string][]terraformutils.Resource, len(listed))
	var leftOut []engine.External
	for service, resources := range listed {
		for _, res := range resources {
			id, importable := importID(res)
			if !importable {
				// importsByDir leaves it out and counts it.
				selected[service] = append(selected[service], res)
				continue
			}
			typ := res.InstanceInfo.Type
			reason, excluded := defaults[typ+" "+res.InstanceState.ID]
			var d selection.Decision
			switch {
			case f != nil && f.Has(typ, id):
				d = f.Decide(typ, id, listedName(res.ResourceName))
			case excluded:
				d = selection.Decision{Reason: reason}
			case f != nil:
				d = f.Decide(typ, id, listedName(res.ResourceName))
			default:
				d = selection.Decision{Include: true}
			}
			if d.Include {
				selected[service] = append(selected[service], res)
				continue
			}
			r.discovered[typ]++
			r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: typ, ID: id, Reason: d.Reason})
			leftOut = append(leftOut, engine.External{Type: typ, ID: id})
		}
	}
	// In a stable order: listed is a map, and the order names data sources.
	sort.Slice(leftOut, func(i, j int) bool {
		if leftOut[i].Type != leftOut[j].Type {
			return leftOut[i].Type < leftOut[j].Type
		}
		return leftOut[i].ID < leftOut[j].ID
	})
	return selected, leftOut
}

// addDiscovered adds the listed resources Terraform can import to the
// selection file discover writes, included unless the provider's defaults
// exclude them.
func (r *engineRun) addDiscovered(listed map[string][]terraformutils.Resource, defaults map[string]string, importID func(terraformutils.Resource) (string, bool)) {
	for _, resources := range listed {
		for _, res := range resources {
			id, importable := importID(res)
			if !importable {
				continue
			}
			reason := defaults[res.InstanceInfo.Type+" "+res.InstanceState.ID]
			r.listed = append(r.listed, selection.Resource{
				Type:    res.InstanceInfo.Type,
				ID:      id,
				Name:    listedName(res.ResourceName),
				Include: reason == "",
				Reason:  reason,
			})
		}
	}
}

// writeSelection writes what discover listed.
func (r *engineRun) writeSelection() error {
	path := r.options.Selection
	if path == "" {
		path = DefaultSelectionFile
	}
	f, err := selection.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f = &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: r.listed}
	case err != nil:
		return err
	default:
		// Running discover again: keep the decisions people made.
		added, dropped := f.Merge(r.listed)
		log.Printf("updated %s: %d new resources marked new: true for review, %d no longer found and dropped", path, added, dropped)
	}
	if err := f.Save(path); err != nil {
		return err
	}
	excluded := 0
	for _, res := range r.listed {
		if !res.Include {
			excluded++
		}
	}
	log.Printf("listed %d resources into %s, %d of them excluded by default; review it, then import with --engine=terraform --selection %s", len(r.listed), path, excluded, path)
	return nil
}

// backendState is the --managed-state source that stands for the state of
// the configured backend.
const backendState = "backend"

// excludeManaged adds to defaults the listed resources Terraform already
// manages, according to the state in sources (see managed.Load), so that
// an import leaves them out unless a selection file says otherwise.
func excludeManaged(ctx context.Context, run *engineRun, sources []string, listed map[string][]terraformutils.Resource, defaults map[string]string, importID func(terraformutils.Resource) (string, bool)) (map[string]string, error) {
	if len(sources) == 0 {
		return defaults, nil
	}
	resolved := make([]string, 0, len(sources))
	for _, s := range sources {
		if s != backendState {
			resolved = append(resolved, s)
			continue
		}
		if run.backend == nil || run.backend.S3 == nil {
			return nil, errors.New("--managed-state=backend needs an S3 backend in the configuration file")
		}
		s3 := run.backend.S3
		resolved = append(resolved, fmt.Sprintf("s3://%s/%s?region=%s", s3.Bucket, s3.KeyPrefix, s3.Region))
	}
	state, err := managed.Load(ctx, resolved, managed.NewS3)
	if err != nil {
		return nil, err
	}
	if defaults == nil {
		defaults = map[string]string{}
	}
	for _, resources := range listed {
		for _, res := range resources {
			id, _ := importID(res)
			if where, ok := state.Lookup(res.InstanceInfo.Type, id, res.InstanceState.ID); ok {
				defaults[res.InstanceInfo.Type+" "+res.InstanceState.ID] = managed.Reason + " (" + where + ")"
			}
		}
	}
	return defaults, nil
}
