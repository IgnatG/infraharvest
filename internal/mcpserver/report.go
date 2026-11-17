// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/report"
)

// ReportInput is the report tool's input.
type ReportInput struct {
	Out string `json:"out" jsonschema:"the output directory of an import"`
	// Kind is the report to read: reportImport or reportDrift.
	Kind string `json:"kind,omitempty" jsonschema:"import for the import's report (report.md), drift for the drift report (drift.md); default: the import's, or the drift report when the output directory has only that"`
}

// The kinds of report the report tool reads.
const (
	reportImport = "import"
	reportDrift  = "drift"
)

func (t *tools) report(_ context.Context, _ *mcp.CallToolRequest, in ReportInput) (*mcp.CallToolResult, any, error) {
	if in.Out == "" {
		return nil, nil, errors.New("out is required")
	}
	if in.Kind != "" && in.Kind != reportImport && in.Kind != reportDrift {
		return nil, nil, fmt.Errorf("kind must be %s or %s, not %q", reportImport, reportDrift, in.Kind)
	}
	out, err := t.within("out", in.Out)
	if err != nil {
		return nil, nil, err
	}
	in.Out = out
	path := filepath.Join(in.Out, report.Dir, "report.md")
	drift := filepath.Join(in.Out, report.Dir, report.DriftMarkdownFile)
	switch in.Kind {
	case reportDrift:
		path = drift
	case "":
		// An output directory drift checked, whose import ran elsewhere,
		// has the drift report only.
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Lstat(drift); err == nil {
				path = drift
			}
		}
	}
	// Opened as any file the agent names is (see openWithin):
	// .infraharvest or the report could be a symbolic link that leads out
	// of the root, such as one a cloned repository plants to a credentials
	// file, or a hard link to one.
	path, f, err := t.openWithin("report", path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("no report in %s: %w", in.Out, err)
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	content, size, err := readHead(f, maxReport)
	if err != nil {
		return nil, nil, fmt.Errorf("no report in %s: %w", in.Out, err)
	}
	text := string(content)
	if left := size - int64(len(content)); left > 0 {
		text += fmt.Sprintf("\n\n[%d of the report's %d bytes are left out here: the full report is %s]", left, size, path)
	}
	return textResult(text), nil, nil
}

// maxReport is the most bytes of a report the report tool returns: a
// report is mostly counts by type, but one of a large import could still
// fill an agent's context.
const maxReport = 256 << 10

// readHead reads at most limit bytes of f, cut after the last whole line
// that fits, or else the last whole character, and returns them with the
// file's size.
func readHead(f *os.File, limit int) ([]byte, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	content, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, 0, err
	}
	size := max(info.Size(), int64(len(content)))
	if len(content) <= limit {
		return content, size, nil
	}
	return content[:cutAt(content, limit)], size, nil
}

// cutAt returns where to cut b, which is longer than limit, so that what
// is kept fits in limit and ends with a whole line, or else a whole
// character.
func cutAt(b []byte, limit int) int {
	cut := limit
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	if i := bytes.LastIndexByte(b[:cut], '\n'); i >= 0 {
		cut = i + 1
	}
	return cut
}
