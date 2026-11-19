// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package schemas holds the JSON Schemas of the documents infraharvest
// export writes, so that a hosted control plane can validate what it
// receives, and those of the files drift writes and reads.
package schemas

import "embed"

// FS has the schemas: inventory-v1.schema.json, run-v1.schema.json,
// gate-v1.schema.json, graph-v1.schema.json,
// drift-summary-v1.schema.json, ocsf-inventory-v1.schema.json and
// ocsf-findings-v1.schema.json, the documents export writes (the extension
// the last two use is under ocsf/, and isn't embedded); and
// drift-v1.schema.json, which describes drift.json, and
// drift-decisions-v1.schema.json, the decisions file.
//
//go:embed *.schema.json
var FS embed.FS
