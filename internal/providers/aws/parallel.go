// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "github.com/IgnatG/infraharvest/internal/providers/workers"

// resourceParallel is how many listed resources a lister reads at once
// when each takes calls of its own, such as a key's grants or a load
// balancer's listeners. The SDK configuration's rate limit and retries
// still pace the calls (see Service.sdkConfig).
const resourceParallel = 4

// inParallel calls read for each index below n, at most parallel at once,
// and returns once every call has. read writes what it reads into a slot
// of its index, so that the caller records the answers in the listed
// order, which the output keeps. A panic in a read
// is raised again on the caller's goroutine once every read has returned,
// so that it fails the lister and not the process.
func inParallel(n, parallel int, read func(i int)) {
	g := workers.New(max(parallel, 1))
	for i := range n {
		g.Go(func() { read(i) })
	}
	g.Wait()
}
