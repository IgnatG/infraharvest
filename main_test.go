// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"log"
	"testing"
)

// Debug and trace messages from client libraries are hidden; messages that
// only mention a level are not.
func TestLogWriterHidesOnlyDebugAndTraceMessages(t *testing.T) {
	for _, flags := range []int{0, log.LstdFlags, log.LstdFlags | log.Lmicroseconds} {
		var out bytes.Buffer
		logger := log.New(logWriter{w: &out}, "", flags)

		logger.Print("[DEBUG] GET /v1/things HTTP/1.1")
		logger.Print("[TRACE] request body")
		logger.Print(`aws: skipping bucket "[DEBUG] logs": not selected`)
		logger.Print("generating configuration for 3 resources")

		got := out.String()
		for _, hidden := range []string{"GET /v1/things", "request body"} {
			if bytes.Contains(out.Bytes(), []byte(hidden)) {
				t.Errorf("flags %d: %q not hidden in %q", flags, hidden, got)
			}
		}
		for _, kept := range []string{`bucket "[DEBUG] logs"`, "generating configuration"} {
			if !bytes.Contains(out.Bytes(), []byte(kept)) {
				t.Errorf("flags %d: %q hidden from %q", flags, kept, got)
			}
		}
	}
}
