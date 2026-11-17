// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// WriteFiles writes report.json, the whole report as WriteJSON writes it,
// and report.md into the report directory under outputDir, and the
// templates of the CloudFormation stacks the import is scoped to under
// cfn.TemplatesDir.
func (r *Report) WriteFiles(outputDir string) error {
	dir := filepath.Join(outputDir, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	jsonContent, err := marshal(r)
	if err != nil {
		return err
	}
	for name, content := range map[string][]byte{
		"report.json": jsonContent,
		"report.md":   []byte(r.Markdown()),
	} {
		if err := fsutil.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return err
		}
	}
	return r.CloudFormationStacks.WriteTemplates(outputDir)
}

// WriteJSON writes the whole report as one JSON document.
func (r *Report) WriteJSON(w io.Writer) error {
	content, err := marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}

func marshal(v any) ([]byte, error) {
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// Markdown renders the report for people.
func (r *Report) Markdown() string {
	var b strings.Builder
	r.summaryMarkdown(&b)
	r.typesMarkdown(&b)
	r.tagsMarkdown(&b)
	d := r.directoryLines()
	writeSection(&b, "Left out", "Terraform couldn't import these resources or generate valid configuration for them. Each directory's `rejected.hcl` has their blocks and errors.", d.leftOut)
	writeSection(&b, "Secrets to set", "Set these variables before planning, from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with `TF_VAR_<name>`: infraharvest never writes secret values into its output.", d.secrets)
	writeSection(&b, "Warnings", "Look at these before using the configuration.", d.warnings)
	r.findingsMarkdown(&b)
	writeSection(&b, "No longer in the cloud", "The roots have these resources, but the import didn't find them, though it listed every resource of their type where they were: they have been deleted since, and the next plan of the root would create them again. Delete each one's import block, if the root's `imports.tf` still has it, then either delete its resource block, and anything that refers to it, and plan; or, if it should exist, plan to create it again. The import changed nothing the roots have.", r.notInCloudLines())
	writeSection(&b, "Moved", "Generated again, these roots have these resources at other addresses: their moved blocks, in `moves.tf`, move them in state, so their next plans destroy nothing.", r.movedLines())
	writeSection(&b, "Handed over", "These resources go to other roots now. The removed blocks in `moves.tf` of the roots they were in make those roots' states forget them, without destroying them, and the roots they go to import them. Apply the root each was in first, then the root it goes to.", r.departedLines())
	writeSection(&b, "Kept where they were", "An earlier import generated these roots, so they keep their directories, and the resources in them, though the configuration file's environments would put those under `envs/<env>/` now: a root's state key comes from its path. To lay them out by environment, import into a new `--out`, or generate them again with `--regenerate`, which hands their resources over to the roots of their environments where removed blocks can take them all, the log saying why not.", r.keptRootLines())
	merged, apart := r.mergeLines()
	writeSection(&b, "Environments merged", "The environments of each of these stacks share one root, with their own values in `vars/<env>/`: each environment's plan of it, with its own credentials, imports what its own root did and changes nothing else. The roots they had are kept under `.infraharvest/premerge/`. Each merged root's README says how to plan and apply an environment.", merged)
	writeSection(&b, "Environments kept apart", "The environments of each of these stacks keep a root each, as they were generated: merging them into one root wouldn't have planned the same.", apart)
	writeSection(&b, "Handovers not finished", "One of the two roots of each of these handovers failed: do what each line says before applying either root.", r.incompleteHandoverLines())
	r.CloudFormationStacks.Markdown(&b)
	writeSection(&b, "Modules", "These resources are managed through module calls, each kept because it planned the same as the resources it replaced.", d.modules)
	writeSection(&b, "Not moved into a module", "A module couldn't manage these resources the same way, so they stay in the root.", d.declined)
	writeSection(&b, "Not importable", "infraharvest doesn't import these resource types.", r.skippedLines())
	writeSection(&b, "Excluded", "The selection left these resources out.", r.excludedLines())
	writeSection(&b, "Failed checks", "These directories were generated, but failed a check of the verification gate. Each directory's README lists its checks.", proseLines(r.FailedChecks()))
	writeSection(&b, "Skipped checks", "These checks couldn't run on these directories, so they checked nothing. They don't fail the import.", proseLines(r.SkippedChecks()))
	writeSection(&b, "Terraform plans", "The plans generating each directory ran, by step: each imports every resource of the directory again.", d.plans)
	writeSection(&b, "Failed directories", "Nothing in these directories was imported.", d.errors)
	writeSection(&b, "Failures", "These parts of the import couldn't run, or ran only in part: services that couldn't be listed, accounts or projects that couldn't be reached, tags or default selections that couldn't be read, and what stopped the run.", proseLines(r.Failures))
	return b.String()
}

// Read reads the report an import wrote into outputDir (see WriteFiles).
func Read(outputDir string) (*Report, error) {
	content, err := os.ReadFile(filepath.Join(outputDir, Dir, "report.json"))
	if err != nil {
		return nil, err
	}
	return Parse(content)
}

// Parse reads content, a report.json an import wrote (see Read).
func Parse(content []byte) (*Report, error) {
	r := &Report{}
	if err := json.Unmarshal(content, r); err != nil {
		return nil, fmt.Errorf("report.json: %w", err)
	}
	if r.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("report schema version %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	return r, nil
}
