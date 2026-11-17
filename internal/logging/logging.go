// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package logging is how infraharvest logs and what it keeps out of its
// output. It logs through log/slog with a handler that writes the text
// the standard logger wrote (see NewHandler), at Info, or at Debug with
// --verbose; it drops the debug and trace messages client libraries log,
// some of which hold whole HTTP requests; and it escapes the control
// characters of names and values read from a cloud (see Sanitize) and
// redacts the credentials of HTTP requests (see RedactHTTP).
package logging
