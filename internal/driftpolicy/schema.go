// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"go.yaml.in/yaml/v3"

	"github.com/IgnatG/infraharvest/schemas"
)

// decisionsSchemaFile is the schema of a decisions file's content, in the
// schemas package.
const decisionsSchemaFile = "drift-decisions-v1.schema.json"

// decisionsSchema resolves the schema of a decisions file once.
var decisionsSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	content, err := schemas.FS.ReadFile(decisionsSchemaFile)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(content, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", decisionsSchemaFile, err)
	}
	return s.Resolve(nil)
})

// checkSchema checks content, the YAML of a decisions file, read as JSON,
// against its schema (schemas/drift-decisions-v1.schema.json).
func checkSchema(content []byte) error {
	schema, err := decisionsSchema()
	if err != nil {
		return err
	}
	var generic any
	if err := yaml.Unmarshal(content, &generic); err != nil {
		return err
	}
	// The validator takes what encoding/json decodes, not what YAML does:
	// integers and timestamps, say, as numbers and text.
	encoded, err := json.Marshal(generic)
	if err != nil {
		return fmt.Errorf("a key that isn't text: %w", err)
	}
	var instance any
	if err := json.Unmarshal(encoded, &instance); err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("doesn't match %s: %s", decisionsSchemaFile, innermost(err))
	}
	return nil
}

// innermost is what the validator says of the schema that failed, without
// the chain of schemas it went through to reach it: the last "validating
// <schema path>:" of its message, and what follows.
func innermost(err error) string {
	const marker = "validating /"
	s := err.Error()
	if i := strings.LastIndex(s, marker); i >= 0 {
		return strings.TrimSpace(s[i+len("validating "):])
	}
	return s
}
