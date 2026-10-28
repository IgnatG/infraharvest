// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
)

// opaCapabilities are the capabilities of OPA 1.21.1, the release
// conftest 0.71.1 is built with, as OPA publishes them
// (capabilities/v1.21.1.json in its repository): its built-in
// functions, keywords and features. The file is copied unchanged and is
// OPA's, under Apache-2.0, as REUSE.toml and NOTICE say.
//
//go:embed conftest_capabilities.json
var opaCapabilities []byte

// networkBuiltins are OPA's built-in functions that reach the network: a
// policy could send what it reads, the configuration, with them.
var networkBuiltins = []string{"http.send", "net.lookup_ip_addr"}

// conftestBuiltins are the built-in functions conftest adds to OPA's,
// which read configuration files, with their types as conftest declares
// them; OPA's capabilities don't have them, and a policy may call them.
const conftestBuiltins = `[
	{"name": "parse_config", "decl": {"type": "function", "args": [{"type": "string"}, {"type": "string"}], "result": {"type": "any"}}},
	{"name": "parse_config_file", "decl": {"type": "function", "args": [{"type": "string"}], "result": {"type": "any"}}},
	{"name": "parse_combined_config_files", "decl": {"type": "function", "args": [{"type": "array", "dynamic": {"type": "string"}}], "result": {"type": "any"}}}
]`

// conftestCapabilities returns the capabilities conftest runs policies
// with (--capabilities): OPA's, and conftest's own built-in functions,
// but for those that reach the network, and with no host to reach
// (allow_net), so that a policy that calls one doesn't compile. A
// conftest built with a later OPA runs policies with OPA 1.21.1's
// built-in functions all the same: a policy that calls a newer one
// doesn't compile either. They are worked out once, on first use.
var conftestCapabilities = sync.OnceValues(func() ([]byte, error) {
	var capabilities map[string]json.RawMessage
	var builtins, own []json.RawMessage
	if err := json.Unmarshal(opaCapabilities, &capabilities); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilities["builtins"], &builtins); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(conftestBuiltins), &own); err != nil {
		return nil, err
	}
	builtins = slices.DeleteFunc(append(builtins, own...), func(b json.RawMessage) bool {
		var named struct {
			Name string `json:"name"`
		}
		return json.Unmarshal(b, &named) != nil || slices.Contains(networkBuiltins, named.Name)
	})
	var err error
	if capabilities["builtins"], err = json.Marshal(builtins); err != nil {
		return nil, err
	}
	capabilities["allow_net"] = json.RawMessage("[]")
	return json.Marshal(capabilities)
})
