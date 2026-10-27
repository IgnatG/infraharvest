// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/sqladmin/v1"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// runCaches are what the imports of one run share, kept with the run's
// limiter (see cachesOf): what each project's imports list once (see
// projectLists), what the search rejected, and what cloudsql listed in
// part. Another run, such as a server's next, lists afresh and sees none
// of them.
type runCaches struct {
	// searches are the searches the run has made (see searchOnce).
	searches projectLists[[]*cloudasset.ResourceSearchResult]
	// instances are the instances the run has listed (see instancesOnce).
	instances projectLists[instanceList]
	// autopilotClusters are the Autopilot clusters the run has listed (see
	// autopilotClustersOnce).
	autopilotClusters projectLists[map[string]bool]
	// sqlInstances are the Cloud SQL instance lists the run has made (see
	// sqlInstancesOnce).
	sqlInstances projectLists[[]*sqladmin.DatabaseInstance]

	// mu guards rejectedTypes and unlistedSQLDatabases, which services
	// listing at once read and write.
	mu sync.Mutex
	// rejectedTypes are the asset types the search has rejected in the
	// run: the searches of other projects leave them out rather than find
	// them again.
	rejectedTypes map[string]bool
	// unlistedSQLDatabases are the imports, by sqlDatabasesKey, whose last
	// listing of the databases left out those of an instance that
	// couldn't serve the list (see sqlInstanceNotServing). The run keeps
	// them, as rejectedTypes, since the lister and the provider asked for
	// its partial types are not the same: every service of an import has
	// a provider of its own.
	unlistedSQLDatabases map[string]bool
}

// runCachesKey is the key of a run's caches kept with its limiter.
type runCachesKey struct{}

// cachesOf returns the caches of ctx's run: those kept with the run's
// limiter (see ratelimit.FromContext), made the first time it is asked
// for.
func cachesOf(ctx context.Context) *runCaches {
	return ratelimit.Kept(ratelimit.FromContext(ctx), runCachesKey{}, func() *runCaches {
		return &runCaches{rejectedTypes: map[string]bool{}, unlistedSQLDatabases: map[string]bool{}}
	})
}

// projectLists are lists of one kind that a run has made, by project, each
// made once: the imports of a project's regions and of global share a
// list, which they only read.
type projectLists[T any] struct {
	mu    sync.Mutex
	byKey map[string]*projectList[T]
}

// projectList is one project's list: its value, or its error, once it
// has been made. mu is held while it runs, so that an import that wants
// it meanwhile waits for it rather than listing too.
type projectList[T any] struct {
	mu    sync.Mutex
	done  bool
	value T
	err   error
}

// once returns what list returns for project, calling it only the first
// time the run asks. A list that fails is kept too, so that the
// imports of the project's other regions don't make the failing call
// again, each after its retries (see retryTransport): only one that its
// context ended is not, so the next import lists again.
func (l *projectLists[T]) once(project string, list func() (T, error)) (T, error) {
	l.mu.Lock()
	if l.byKey == nil {
		l.byKey = map[string]*projectList[T]{}
	}
	p, ok := l.byKey[project]
	if !ok {
		p = &projectList[T]{}
		l.byKey[project] = p
	}
	l.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.value, p.err
	}
	value, err := list()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		var zero T
		return zero, err
	}
	p.value, p.err, p.done = value, err, true
	return value, err
}

// forget drops every list kept, for tests.
func (l *projectLists[T]) forget() {
	l.mu.Lock()
	l.byKey = nil
	l.mu.Unlock()
}
