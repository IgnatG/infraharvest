// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/export"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/report"
)

// exportFlags are the values of the flags of export that shape a document.
type exportFlags struct {
	document    string
	command     string
	requestID   string
	dropTags    []string
	dropAllTags bool
	hashIDs     bool
	hashKeyFile string
	startedAt   string
	finishedAt  string
}

func newExportCmd() *cobra.Command {
	var (
		f           exportFlags
		outFile     string
		outFlag     string
		printSchema bool
	)
	cmd := &cobra.Command{
		Use:   "export [output-directory]",
		Short: "Write the metadata of an import that a control plane may receive (default directory: " + report.DefaultOut + ")",
		Long: "Write one JSON document of the metadata of an import, from the report in its\n" +
			"output directory (default: " + report.DefaultOut + ") and the inventory discover saved there:\n" +
			"inventory (each resource's type, ID, account and who manages it, with tags),\n" +
			"run (command, outcome and, for an import, exit code and counts) or gate (each\n" +
			"check of the verification gate, with reason codes). Nothing is uploaded and no\n" +
			"network call is made: the document goes to standard output, or to --out-file,\n" +
			"for you to send or keep.\n" +
			"It holds no attributes, values or addresses. Tags whose key or value looks like\n" +
			"a secret are dropped; --drop-tag and --drop-tags drop more, and --hash-ids\n" +
			"replaces IDs and names with HMAC-SHA256 hashes under a key only you hold.\n" +
			"The report holds no times, so pass --started-at and --finished-at (RFC 3339) to\n" +
			"put them in the run document; --finished-at is also the inventory's\n" +
			"generated_at, which is the time of the export without it.\n" +
			"The run document describes the run the output directory's report is of: an\n" +
			"import's report.json, or, if it has none, the drift report drift.json; --command\n" +
			"names the one to describe when it has both (default: import). --request-id puts\n" +
			"the ID a control plane gave to ask for the run into it.\n" +
			"--print-schema prints the JSON Schema of the document instead.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var content []byte
			var err error
			if printSchema {
				content, err = export.Schema(f.document)
			} else {
				var out string
				if out, err = outDir(cmd, args, outFlag); err != nil {
					return err
				}
				content, err = buildExport(out, f)
			}
			if err != nil {
				return err
			}
			if outFile == "" {
				_, err = cmd.OutOrStdout().Write(content)
				return err
			}
			return fsutil.WriteFile(outFile, content, 0o644)
		},
	}
	addOutFlag(cmd, &outFlag)
	cmd.Flags().StringVar(&f.document, "schema", export.DocInventory, "document to write: "+strings.Join(export.Documents, ", "))
	cmd.Flags().BoolVar(&printSchema, "print-schema", false, "print the JSON Schema of the document --schema names, not the document")
	cmd.Flags().StringVar(&outFile, "out-file", "", "file to write the document to (default: standard output)")
	cmd.Flags().StringSliceVar(&f.dropTags, "drop-tag", nil, "tag key to leave out of the inventory; repeat or comma-separate for several")
	cmd.Flags().BoolVar(&f.dropAllTags, "drop-tags", false, "leave all tags out of the inventory")
	cmd.Flags().BoolVar(&f.hashIDs, "hash-ids", false, "replace IDs and names in the inventory with HMAC-SHA256 hashes under a key you hold: from "+export.KeyEnv+", or --hash-key-file")
	cmd.Flags().StringVar(&f.hashKeyFile, "hash-key-file", "", "file with the key --hash-ids hashes with (at least 16 bytes), instead of "+export.KeyEnv)
	cmd.Flags().StringVar(&f.startedAt, "started-at", "", "when the import started, RFC 3339 (such as 2026-11-03T09:30:00Z): the run document's started_at")
	cmd.Flags().StringVar(&f.finishedAt, "finished-at", "", "when the import finished, RFC 3339: the run document's finished_at and the inventory's generated_at (default for generated_at: now)")
	cmd.Flags().StringVar(&f.command, "command", "", "command whose run the run document describes: "+strings.Join(export.Commands, " or ")+" (default: the one whose report the output directory has, import if both)")
	cmd.Flags().StringVar(&f.requestID, "request-id", "", "ID a control plane gave to ask for this run, for the run document's request_id: 1 to 128 letters, digits, and . _ : -")
	return cmd
}

// buildExport builds the document of the output directory out.
func buildExport(out string, f exportFlags) ([]byte, error) {
	opts := export.Options{
		Out: out, Document: f.document, DropTags: f.dropTags, DropAllTags: f.dropAllTags,
		Command: f.command, RequestID: f.requestID,
	}
	var err error
	if f.startedAt != "" {
		if opts.StartedAt, err = export.ParseTime("--started-at", f.startedAt); err != nil {
			return nil, err
		}
	}
	if f.finishedAt != "" {
		if opts.FinishedAt, err = export.ParseTime("--finished-at", f.finishedAt); err != nil {
			return nil, err
		}
	}
	switch {
	case f.hashIDs:
		key, err := export.ReadKey(f.hashKeyFile, os.Getenv)
		if err != nil {
			return nil, err
		}
		opts.HashKey = key
	case f.hashKeyFile != "":
		return nil, errors.New("--hash-key-file needs --hash-ids")
	}
	content, err := export.Build(opts)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return content, nil
}
