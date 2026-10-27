// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftSettings are the settings of a configuration file drift takes, of
// those the import takes, as they mean the same for it: how Terraform runs,
// and the catalogue the import used. It leaves the import's others, such
// as the services and the selection, to the import.
var driftSettings = []string{"engine", "terraform-path", "engine-timeout", "catalogue"}

func newDriftCmd() *cobra.Command {
	options := importer.DriftOptions{Provider: providers.New, Names: providers.Names(), Backends: providers.Backends()}
	var configFile string
	cmd := &cobra.Command{
		Use:   "drift [output-directory]",
		Short: "Find what changed in the cloud since the generated roots were applied",
		Long: "Check every root an import generated (default directory: " + report.DefaultOut + ")\n" +
			"for what changed in the cloud since it was applied. Each root is initialised\n" +
			"with its backend and its state pulled; a private copy is then refreshed once,\n" +
			"and planned again with the code, to tell the changes the next apply would\n" +
			"revert from those the code absorbs. Nothing is written to the state, and\n" +
			"plans don't lock it, so read access to it is enough. S3, GCS and azurerm\n" +
			"backends and local state are supported; HCP Terraform and other remote\n" +
			"backends are refused. A root environments share is checked once per\n" +
			"environment --environment names, or every one with --environment all, which\n" +
			"roots of more than one environment need, with its backend settings,\n" +
			"vars/<env>/backend.hcl, or without a backend its workspace's local state, and\n" +
			"its var files; as they share its code, an accepted change is written into\n" +
			"vars/<env>/terraform.tfvars where a variable sets the value, else needs a\n" +
			"hand edit.\n\n" +
			"Without --accept, --rules or --decisions, the code isn't changed either. With\n" +
			"them, the cloud's values of the changes they accept are written into the code,\n" +
			"once the plan of the edited copy, without refreshing again, shows each accepted\n" +
			"resource taking them and nothing else changing; the others are kept, and the\n" +
			"next apply puts them back. Secrets are never written.\n\n" +
			"--config takes the import's configuration file: its settings drift has, engine,\n" +
			"terraform-path, engine-timeout and catalogue, apply where the command line\n" +
			"doesn't give them, and the rest are left to the import. Its own modules in\n" +
			"directories are left to their owners by the checks of the edited code, as by\n" +
			"the import's.\n\n" +
			"--write-decisions writes a decisions file, drift.yaml in the output directory,\n" +
			"the --decisions file, or the file --write-decisions=<file> names, with an entry\n" +
			"per change to accept or keep; edit it, and apply it with --decisions. A file\n" +
			"--decisions doesn't name isn't applied. Its entries decide over its rules and\n" +
			"defaults, and --accept and --keep decide over the file. An entry decided for\n" +
			"other values, as the cloud changed again since, isn't applied.\n\n" +
			"The report goes into report/drift.json and report/drift.md. Exit code 0 means\n" +
			"every root was checked, 2 that one couldn't be; with --detailed-exitcode, 4\n" +
			"means drift is left, and 1 that an accepted change failed verification.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile != "" {
				f, err := config.Load(configFile, providers.Profiles())
				if err != nil {
					return err
				}
				if err := f.ApplyShared(cmd.Flags(), cmd.Name(), driftSettings); err != nil {
					return fmt.Errorf("%s: %w", configFile, err)
				}
				options.Modules = f.Modules
			}
			options.Out = report.DefaultOut
			if len(args) == 1 {
				options.Out = args[0]
			}
			ctx, stop := importer.InterruptContext(cmd.Context())
			defer stop()
			return importer.Interrupted(ctx, importer.Drift(ctx, options, cmd.OutOrStdout()))
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "the import's configuration file, whose settings drift has, engine, terraform-path, engine-timeout and catalogue, apply where not given on the command line, and whose own modules the checks of the edited code leave to their owners")
	providers.AddEngineFlags(cmd.Flags(), providers.EngineFlags{
		Engine: &options.Engine, TerraformPath: &options.TerraformPath, EngineTimeout: &options.EngineTimeout,
		Format: &options.Format, Catalogue: &options.Catalogue, ForceUnlock: &options.ForceUnlock, Environments: &options.Environments,
		FormatHelp:      "text prints a line per root; json prints drift.json",
		CatalogueHelp:   ": those the import used, whose arguments kept only in state aren't drift",
		EnvironmentHelp: "for a root environments share, the environments to check it as, each with its backend settings and var files, with the credentials drift runs with, or all for every one under its vars/, if they reach every one; roots of more than one environment need it",
	})
	cmd.Flags().IntVar(&options.Parallel, "parallel", 1, "how many roots to check at once; each runs its own Terraform, so memory use grows with it")
	cmd.Flags().StringArrayVar(&options.Roots, "root", nil, "check only the roots whose path in the output directory, or a directory it is in, matches this pattern, such as aws/*/eu-west-2 (repeatable)")
	cmd.Flags().Var(&driftRules{rules: &options.Rules, accept: true}, "accept", "write the cloud's values of the changes this rule matches into the code: all, or attribute=, type=, address= or root= with a pattern, such as attribute=tags.* (repeatable; with --keep and --rules, the last rule that matches a change decides)")
	cmd.Flags().Var(&driftRules{rules: &options.Rules}, "keep", "keep the code's values of the changes this rule matches, which the next apply puts back, as it does for changes no rule matches: all, or a rule as --accept takes it (repeatable)")
	cmd.Flags().Var(&driftRuleFiles{rules: &options.Rules}, "rules", "accept and keep rules from this file, one a line: accept or keep, then a rule as --accept takes it, such as keep type=aws_security_group; a rule alone is accepted, and blank lines and lines starting with # are skipped (repeatable; the rules join those of --accept and --keep in the order given)")
	cmd.Flags().StringVar(&options.Decisions, "decisions", "", "decide the changes by this decisions file, which --write-decisions writes: each change's entry, else its rules and defaults; --accept and --keep decide after it")
	cmd.Flags().Var(&writeDecisions{on: &options.WriteDecisions, path: &options.WriteDecisionsTo}, "write-decisions", "write this run's changes into the decisions file, --write-decisions=<file>, else --decisions, else "+driftpolicy.DefaultDecisionsFile+" in the output directory, which isn't applied unless --decisions names it: entries whose change is unchanged keep their decisions, new changes and those that changed again are undecided, and changes no longer found are dropped")
	cmd.Flags().Lookup("write-decisions").NoOptDefVal = "true"
	cmd.Flags().BoolVar(&options.DetailedExitCode, "detailed-exitcode", false, "exit with 4 when a change is left that the next apply reverts or that can't be assessed, 1 when an accepted change failed verification, 0 when none")
	cmd.Flags().BoolVar(&options.UntrustedRoots, "untrusted-roots", false, "Terraform ignores the roots' .terraform directories and any filesystem mirror, such as a terraform.d directory, and installs providers from their registries alone, as the MCP server runs drift; the roots' providers and modules still run, so check what they use first")
	cmd.Flags().StringVar(&options.RootsDigest, "roots-digest", "", "run only if the files Terraform reads under the output directory still have this digest, as the MCP server checked them")
	_ = cmd.Flags().MarkHidden("roots-digest")
	return cmd
}

// driftRules is the value of --accept or --keep: both add to one list, in
// the order given, as the last rule that matches a change decides.
type driftRules struct {
	rules  *[]driftpolicy.Rule
	accept bool
}

func (r *driftRules) Set(s string) error {
	rule, err := driftpolicy.ParseRule(s, r.accept)
	if err != nil {
		return err
	}
	*r.rules = append(*r.rules, rule)
	return nil
}

func (r *driftRules) String() string {
	if r.rules == nil {
		return ""
	}
	var mine []string
	for _, rule := range *r.rules {
		if rule.Accept == r.accept {
			mine = append(mine, rule.String())
		}
	}
	return strings.Join(mine, ",")
}

func (r *driftRules) Type() string { return "rule" }

// driftRuleFiles is the value of --rules: each file's rules, one a line
// as driftpolicy.ParseRuleLine takes it, join the list --accept and
// --keep add to, in the order given.
type driftRuleFiles struct {
	rules *[]driftpolicy.Rule
	files []string
}

func (r *driftRuleFiles) Set(file string) error {
	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	rules, err := driftpolicy.ParseRuleLines(strings.Split(string(content), "\n"))
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	*r.rules = append(*r.rules, rules...)
	r.files = append(r.files, file)
	return nil
}

func (r *driftRuleFiles) String() string { return strings.Join(r.files, ",") }

func (r *driftRuleFiles) Type() string { return "file" }

// writeDecisions is the value of --write-decisions: true or false, as a
// boolean flag takes them, or the decisions file to write, which sets it
// too.
type writeDecisions struct {
	on   *bool
	path *string
}

func (w *writeDecisions) Set(s string) error {
	if s == "" {
		return errors.New("--write-decisions= needs a file, or true or false")
	}
	if on, err := strconv.ParseBool(s); err == nil {
		*w.on, *w.path = on, ""
		return nil
	}
	*w.on, *w.path = true, s
	return nil
}

func (w *writeDecisions) String() string {
	switch {
	case w.on == nil || !*w.on:
		return "false"
	case *w.path != "":
		return *w.path
	}
	return "true"
}

func (w *writeDecisions) Type() string { return "file" }
