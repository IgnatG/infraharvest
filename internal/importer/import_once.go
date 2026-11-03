// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// importOnceEnv switches import-once state on (see engine.ImportOnce) for
// the roots an import generates: the engine imports a root's resources once
// into a private state snapshot that its later plans read, which saves most
// of the plans that read the cloud. It is internal and off by default; the
// generated files are the same either way. Set it to 1 (or true, yes, on).
const importOnceEnv = "INFRAHARVEST_IMPORT_ONCE"

// importOnceFor returns the import-once settings for a root run with tr,
// nil unless importOnceEnv asks for them. The private copy the snapshot is
// built in gets a runner as the root's own.
func importOnceFor(tr terraformRun) *engine.ImportOnce {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(importOnceEnv))) {
	case "1", "true", "yes", "on":
		return &engine.ImportOnce{NewRunner: tr.in}
	}
	return nil
}
