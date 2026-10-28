// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package logging filters what the standard logger writes: it drops the
// debug and trace messages client libraries log, some of which hold whole
// HTTP requests, and keeps everything else.
package logging

import (
	"bytes"
	"io"
	"regexp"
)

// logPrefix matches the date and time the standard logger puts before a
// message, with the fractional seconds it may add.
var logPrefix = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} )?(\d{2}:\d{2}:\d{2}(\.\d+)? )?`)

// Filter returns a writer for log.SetOutput that writes each log entry to
// w, with its control characters escaped (see Sanitize), except the debug
// and trace messages of client libraries.
func Filter(w io.Writer) io.Writer {
	return filter{w: Sanitize(w)}
}

// filter is the writer Filter returns.
type filter struct {
	w io.Writer
}

// Write writes one log entry, or drops it, still reporting it written,
// when its message starts with [TRACE] or [DEBUG]. A message that only
// mentions a level elsewhere is kept.
func (f filter) Write(entry []byte) (int, error) {
	message := entry[len(logPrefix.Find(entry)):]
	if bytes.HasPrefix(message, []byte("[TRACE]")) || bytes.HasPrefix(message, []byte("[DEBUG]")) {
		return len(entry), nil
	}
	return f.w.Write(entry)
}
