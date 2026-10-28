// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// generatedBeforeRetry holds, while retryFallbacks has Terraform generate
// the configuration of the resources it imports again, what the first plan
// generated for the others: a .tf file, so that Terraform reads it and
// generates nothing for them a second time.
const generatedBeforeRetry = "generated_before_retry.tf"

// retryFallbacks imports again, by its fallback ID (see Import.Fallback),
// each resource of imports that diags, the errors of the plan that
// generated the configuration, reject, such as an azapi_resource whose
// pinned API version azapi's schemas don't have: its generated
// configuration goes, its import block takes the fallback ID, and one plan
// generates its configuration again, whose errors it returns in place of
// diags. The imports it retries take their fallback as their ID, so that
// the result reports what they were imported by. Without such a resource,
// or with errors it can't pin on resources, it returns diags as they are,
// for the rounds that leave rejected resources out.
func retryFallbacks(ctx context.Context, tf tfrun.Terraform, dir string, imports []Import, diags []tfjson.Diagnostic) ([]tfjson.Diagnostic, error) {
	byAddress := map[string]int{}
	for i, imp := range imports {
		if imp.Fallback != "" && imp.Fallback != imp.ID {
			byAddress[imp.Type+"."+imp.Name] = i
		}
	}
	if len(byAddress) == 0 || len(diags) == 0 {
		return diags, nil
	}
	errs, err := unresolved("terraform plan", dir, diags)
	if err != nil {
		return diags, nil
	}
	var retried []string
	for address := range errs {
		if _, ok := byAddress[address]; ok {
			retried = append(retried, address)
		}
	}
	if len(retried) == 0 {
		return diags, nil
	}
	slices.Sort(retried)
	generatedPath := filepath.Join(dir, GeneratedFileName)
	generated, err := hclfile.Load(generatedPath)
	if err != nil {
		return nil, err
	}
	importsFile, err := hclfile.Load(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	for _, r := range generated.Resources() {
		if slices.Contains(retried, r.Address) {
			generated.Edit().Body().RemoveBlock(r.Write)
		}
	}
	for _, imp := range importsFile.Imports() {
		if i, ok := byAddress[imp.To]; ok && slices.Contains(retried, imp.To) {
			imp.Write.Body().SetAttributeValue("id", cty.StringVal(imports[i].Fallback))
		}
	}
	if err := generated.Save(); err != nil {
		return nil, err
	}
	if err := importsFile.Save(); err != nil {
		return nil, err
	}
	for _, address := range retried {
		i := byAddress[address]
		// Not the errors themselves, which may quote values the
		// configuration holds, secrets among them.
		log.Printf("%s: Terraform rejected its import by %s; importing it by %s instead", address, imports[i].ID, imports[i].Fallback)
		imports[i].ID, imports[i].Fallback = imports[i].Fallback, ""
	}
	before := filepath.Join(dir, generatedBeforeRetry)
	if err := os.Rename(generatedPath, before); err != nil {
		return nil, err
	}
	diags, _, err = tfrun.Plan(ctx, tf, tfexec.GenerateConfigOut(GeneratedFileName))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("terraform plan: %w", err), os.Rename(before, generatedPath))
	}
	kept, err := os.ReadFile(before)
	if err != nil {
		return nil, err
	}
	again, err := os.ReadFile(generatedPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if len(again) > 0 {
		kept = append(append(kept, '\n'), again...)
	}
	if err := fsutil.WriteFile(generatedPath, kept, 0o644); err != nil {
		return nil, err
	}
	return diags, os.Remove(before)
}
