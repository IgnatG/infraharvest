// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	tfjson "github.com/hashicorp/terraform-json"
)

// ProvidersSchema returns the schemas of the providers the root uses,
// read once for the process for each engine binary and dependency lock
// file: every root of an import pins the same providers, and reading the
// schemas starts each provider and decodes tens of MB for some, such as
// AWS. A root without a lock file, as before its first init, reads them
// itself.
func (r *Runner) ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error) {
	key, ok := schemaKey(r.ExecPath(), r.WorkingDir())
	if !ok {
		return r.Terraform.ProvidersSchema(ctx)
	}
	return processSchemas.get(key, func() (*tfjson.ProviderSchemas, error) {
		return r.Terraform.ProvidersSchema(ctx)
	})
}

// schemaKey identifies the providers' schemas of the root in dir, with
// the engine at execPath: the binary, which has built-in providers and
// writes the schemas, and the root's dependency lock file, which init
// keeps to the providers the root needs, at the versions and checksums
// it installed. ok is false without a lock file.
func schemaKey(execPath, dir string) (key string, ok bool) {
	lock, err := os.ReadFile(filepath.Join(dir, LockFileName))
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(append([]byte(execPath+"\x00"), lock...))
	return hex.EncodeToString(sum[:]), true
}

// maxCachedSchemas bounds how many sets of schemas schemaStore keeps:
// a run has one set per provider version, and another per set of module
// providers, such as google-beta.
const maxCachedSchemas = 4

// schemaStore keeps providers' schemas by schemaKey. Callers only read
// them, so roots that generate in parallel share them.
type schemaStore struct {
	mu      sync.Mutex
	schemas map[string]*tfjson.ProviderSchemas
}

// processSchemas are the schemas the process has read (see
// Runner.ProvidersSchema).
var processSchemas = &schemaStore{}

// get returns the schemas kept under key, or reads them with read and
// keeps them. A failed read isn't kept.
func (s *schemaStore) get(key string, read func() (*tfjson.ProviderSchemas, error)) (*tfjson.ProviderSchemas, error) {
	s.mu.Lock()
	schemas, ok := s.schemas[key]
	s.mu.Unlock()
	if ok {
		return schemas, nil
	}
	schemas, err := read()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schemas == nil || len(s.schemas) >= maxCachedSchemas {
		s.schemas = map[string]*tfjson.ProviderSchemas{}
	}
	s.schemas[key] = schemas
	return schemas, nil
}
