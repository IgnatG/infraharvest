// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// importOnceEnv switches import-once state off (see engine.ImportOnce) for
// the roots an import generates. With it on, the engine imports a root's
// resources once into a private state snapshot that its later plans read,
// which saves most of the plans that read the cloud; the generated files
// are the same either way. It is on by default: set the variable to 0 (or
// false, no, off) to plan every step with import blocks and no state.
const importOnceEnv = "INFRAHARVEST_IMPORT_ONCE"

// importOnceFor returns the import-once settings for a root run with tr,
// nil when importOnceEnv switches them off. The private copy the snapshot
// is built in gets a runner as the root's own.
func importOnceFor(tr terraformRun) *engine.ImportOnce {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(importOnceEnv))) {
	case "0", "false", "no", "off":
		return nil
	}
	return &engine.ImportOnce{NewRunner: tr.in}
}
