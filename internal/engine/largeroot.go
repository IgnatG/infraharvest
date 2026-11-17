// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "github.com/IgnatG/infraharvest/internal/logging"

// LargeRootResources is how many resources a root may have before the
// engine warns that its plans will be slow: Terraform plans and refreshes
// a root as one graph, a few at a time (-parallelism, 10 by default), so a
// root of thousands of resources takes a long time per plan, and the engine
// plans a root several times.
const LargeRootResources = 2000

// warnLargeRoot warns, if the root in dir has more than LargeRootResources
// resources, that its plans will be slow, and what to do about it.
func warnLargeRoot(dir string, resources int) {
	if resources <= LargeRootResources {
		return
	}
	logging.Warnf("%s: the root has %d resources, more than %d, so each plan of it will be slow, and a plan that runs longer than --engine-timeout fails it; split it with --layout, or raise --engine-parallelism (Terraform's default is 10) if the provider's API allows the calls", dir, resources, LargeRootResources)
}
