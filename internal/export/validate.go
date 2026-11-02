// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/IgnatG/infraharvest/schemas"
)

// schemaFile is the file of a document's schema, in the schemas package.
func schemaFile(document string) string {
	return document + "-v1.schema.json"
}

// Schema returns the JSON Schema (2020-12) of a document, as published.
func Schema(document string) ([]byte, error) {
	content, err := schemas.FS.ReadFile(schemaFile(document))
	if err != nil {
		return nil, fmt.Errorf("unknown document %q", document)
	}
	return content, nil
}

var (
	resolvedMu sync.Mutex
	resolved   = map[string]*jsonschema.Resolved{}
)

// resolve returns the document's schema, ready to validate with.
func resolve(document string) (*jsonschema.Resolved, error) {
	resolvedMu.Lock()
	defer resolvedMu.Unlock()
	if rs, ok := resolved[document]; ok {
		return rs, nil
	}
	content, err := Schema(document)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(content, &s); err != nil {
		return nil, fmt.Errorf("%s schema: %w", document, err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", document, err)
	}
	resolved[document] = rs
	return rs, nil
}

// validate checks content, a JSON document, against the schema of
// document. Schemas say additionalProperties false, so a field the schema
// doesn't name fails it.
func validate(document string, content []byte) error {
	rs, err := resolve(document)
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(content, &instance); err != nil {
		return err
	}
	return rs.Validate(instance)
}
