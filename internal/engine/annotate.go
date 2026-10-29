// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// annotation is an edit of generated.tf that adds to or trims what
// Terraform generated, such as sorting its resources or setting
// placeholders, as declared: what it does, which the log gives as why the
// root plans again, whether a change of it needs that plan, and the edit.
type annotation struct {
	name string
	// replan says a change makes the root plan again before anything reads
	// its errors: the edit moves the lines they point at, or resolves some
	// of them. A comment, which changes neither, doesn't.
	replan bool
	// edit edits f, in memory, and reports whether it changed it: one that
	// changed nothing leaves f as it was, for the next to edit.
	edit func(f *hclfile.File) (bool, error)
}

// annotate is the annotation stage: it makes annotations, in order, of
// generated.tf in ws, each on the file as the ones before it left it, and
// returns why the root then plans again: the names of those that changed
// it and need a replan, none if it needs no plan.
func annotate(ws *workspace, annotations []annotation) ([]string, error) {
	var replan []string
	var f *hclfile.File
	for _, a := range annotations {
		// The file is parsed again only once an edit changed it: the syntax
		// tree describes the file as parsed.
		if f == nil {
			var err error
			if f, err = ws.Load(rootfile.GeneratedFileName); err != nil {
				return nil, err
			}
		}
		changed, err := a.edit(f)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		ws.Save(f)
		f = nil
		if a.replan {
			replan = append(replan, a.name)
		}
	}
	return replan, nil
}

// replanSecrets is why the root plans again once hideSecrets moved
// secrets into variables (see planUntilValid).
const replanSecrets = "secrets moved into variables"

// generatedAnnotations are the annotations of generated.tf that come
// before anything reads it. Terraform writes resources in the order its
// graph walk reaches them, which differs between runs: they are sorted,
// so the same estate always gives the same files. Then the arguments other
// resources manage are left out (see Options.Omit), and placeholders and
// lifecycles set. Last, arguments the provider says conflict, which fail
// validation, are settled now, as repair would once a plan reported them:
// after the omissions, so that of a pair the catalogue leaves one out of,
// such as an event rule's is_enabled beside its state, the other stays.
func generatedAnnotations(imports []rootfile.Import, opts Options, hints schemaHints) []annotation {
	omit := withResourceOmissions(opts.Omit, imports)
	return []annotation{
		{name: "resources sorted", replan: true, edit: hclfile.SortResources},
		{name: "arguments left out", replan: true, edit: func(f *hclfile.File) (bool, error) {
			return OmitArguments(f, omit), nil
		}},
		{name: "placeholders set", replan: true, edit: func(f *hclfile.File) (bool, error) {
			return setPlaceholders(f, opts.Placeholders), nil
		}},
		{name: "lifecycles set", replan: true, edit: func(f *hclfile.File) (bool, error) {
			return setLifecycles(f, opts.Lifecycles), nil
		}},
		{name: "conflicting arguments settled", replan: true, edit: func(f *hclfile.File) (bool, error) {
			return resolveConflicts(f, hints), nil
		}},
	}
}

// referencedAnnotations are the annotations of generated.tf once
// postProcess made its references: what is still a literal then is no
// resource's.
func referencedAnnotations() []annotation {
	return []annotation{
		{name: "principals noted", edit: func(f *hclfile.File) (bool, error) {
			return notePrincipals(f), nil
		}},
	}
}

// annotate runs the annotation stage (see annotate) on the root's
// generated.tf, as its directory has it, and writes back what it changed.
func (g *generation) annotate(annotations []annotation) ([]string, error) {
	ws, err := openWorkspace(g.dir)
	if err != nil {
		return nil, err
	}
	replan, err := annotate(ws, annotations)
	if err != nil {
		return nil, err
	}
	return replan, ws.Flush()
}
