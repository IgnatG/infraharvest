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

func newExportCmd() *cobra.Command {
	var (
		document    string
		outFile     string
		printSchema bool
		dropTags    []string
		dropAllTags bool
		hashIDs     bool
		hashKeyFile string
	)
	cmd := &cobra.Command{
		Use:   "export [output-directory]",
		Short: "Write the metadata of an import that a control plane may receive (default directory: " + report.DefaultOut + ")",
		Long: "Write one JSON document of the metadata of an import, from the report in its\n" +
			"output directory (default: " + report.DefaultOut + ") and the inventory discover saved there:\n" +
			"inventory (each resource's type, ID, account and who manages it, with tags),\n" +
			"run (exit code, outcome and counts) or gate (each check of the verification\n" +
			"gate, with reason codes). Nothing is uploaded and no network call is made: the\n" +
			"document goes to standard output, or to --out-file, for you to send or keep.\n" +
			"It holds no attributes, values or addresses. Tags whose key or value looks like\n" +
			"a secret are dropped; --drop-tag and --drop-tags drop more, and --hash-ids\n" +
			"replaces IDs and names with HMAC-SHA256 hashes under a key only you hold.\n" +
			"--print-schema prints the JSON Schema of the document instead.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var content []byte
			var err error
			if printSchema {
				content, err = export.Schema(document)
			} else {
				content, err = buildExport(args, document, dropTags, dropAllTags, hashIDs, hashKeyFile)
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
	cmd.Flags().StringVar(&document, "schema", export.DocInventory, "document to write: "+strings.Join(export.Documents, ", "))
	cmd.Flags().BoolVar(&printSchema, "print-schema", false, "print the JSON Schema of the document --schema names, not the document")
	cmd.Flags().StringVar(&outFile, "out-file", "", "file to write the document to (default: standard output)")
	cmd.Flags().StringSliceVar(&dropTags, "drop-tag", nil, "tag key to leave out of the inventory; repeat or comma-separate for several")
	cmd.Flags().BoolVar(&dropAllTags, "drop-tags", false, "leave all tags out of the inventory")
	cmd.Flags().BoolVar(&hashIDs, "hash-ids", false, "replace IDs and names in the inventory with HMAC-SHA256 hashes under a key you hold: from "+export.KeyEnv+", or --hash-key-file")
	cmd.Flags().StringVar(&hashKeyFile, "hash-key-file", "", "file with the key --hash-ids hashes with (at least 16 bytes), instead of "+export.KeyEnv)
	return cmd
}

// buildExport builds the document of the output directory the arguments
// name.
func buildExport(args []string, document string, dropTags []string, dropAllTags, hashIDs bool, hashKeyFile string) ([]byte, error) {
	out := report.DefaultOut
	if len(args) == 1 {
		out = args[0]
	}
	opts := export.Options{Out: out, Document: document, DropTags: dropTags, DropAllTags: dropAllTags}
	switch {
	case hashIDs:
		key, err := export.ReadKey(hashKeyFile, os.Getenv)
		if err != nil {
			return nil, err
		}
		opts.HashKey = key
	case hashKeyFile != "":
		return nil, errors.New("--hash-key-file needs --hash-ids")
	}
	content, err := export.Build(opts)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return content, nil
}
