// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"io"

	"github.com/IgnatG/infraharvest/internal/report"
)

func PrintReport(out, format string, w io.Writer) error {
	if format != FormatText && format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, format)
	}
	r, err := report.Read(out)
	if err != nil {
		return fmt.Errorf("no report in %s: %w", out, err)
	}
	if format == FormatJSON {
		return r.WriteJSON(w)
	}
	_, err = io.WriteString(w, r.Markdown())
	return err
}
