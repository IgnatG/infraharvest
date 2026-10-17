// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "sync"

// resourceParallel is how many listed resources a lister reads at once
// when each takes calls of its own, such as a key's grants or a load
// balancer's listeners. The SDK configuration's rate limit and retries
// still pace the calls (see Service.sdkConfig).
const resourceParallel = 4

// inParallel calls read for each index below n, at most parallel at once,
// and returns once every call has. read writes what it reads into a slot
// of its index, so that the caller records the answers in the listed
// order, which the output keeps.
func inParallel(n, parallel int, read func(i int)) {
	slots := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			read(i)
		}()
	}
	wg.Wait()
}
