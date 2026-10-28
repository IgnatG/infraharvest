// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	"golang.org/x/sync/singleflight"
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
	// reads makes the callers that miss a key together, as roots that
	// generate in parallel do, wait on one read of it.
	reads singleflight.Group
}

// processSchemas are the schemas the process has read (see
// Runner.ProvidersSchema).
var processSchemas = &schemaStore{}

// get returns the schemas kept under key, or reads them with read and
// keeps them; the callers that miss key while it is read get that read's
// result. A failed read isn't kept: the next caller reads again.
func (s *schemaStore) get(key string, read func() (*tfjson.ProviderSchemas, error)) (*tfjson.ProviderSchemas, error) {
	if schemas, ok := s.cached(key); ok {
		return schemas, nil
	}
	v, err, _ := s.reads.Do(key, func() (any, error) {
		// A read that ended after this caller missed has kept them.
		if schemas, ok := s.cached(key); ok {
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
	})
	if err != nil {
		return nil, err
	}
	return v.(*tfjson.ProviderSchemas), nil
}

// cached returns the schemas kept under key.
func (s *schemaStore) cached(key string) (*tfjson.ProviderSchemas, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schemas, ok := s.schemas[key]
	return schemas, ok
}

// AttributeType returns the type of the attribute at path in resourceType's
// schema, or cty.NilType if the schemas don't describe it.
func AttributeType(schemas *tfjson.ProviderSchemas, resourceType string, path []string) cty.Type {
	if attr := SchemaAttribute(schemas, resourceType, path); attr != nil {
		return attr.AttributeType
	}
	return cty.NilType
}

// SchemaAttribute returns the attribute at path in resourceType's schema,
// or nil if the schemas don't describe it.
func SchemaAttribute(schemas *tfjson.ProviderSchemas, resourceType string, path []string) *tfjson.SchemaAttribute {
	if schemas == nil || len(path) == 0 {
		return nil
	}
	for _, provider := range schemas.Schemas {
		resource, ok := provider.ResourceSchemas[resourceType]
		if !ok || resource.Block == nil {
			continue
		}
		block := resource.Block
		for _, name := range path[:len(path)-1] {
			nested, ok := block.NestedBlocks[name]
			if !ok || nested.Block == nil {
				return nil
			}
			block = nested.Block
		}
		return block.Attributes[path[len(path)-1]]
	}
	return nil
}
