// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
)

// opaVersion is the OPA release opaCapabilities are of. The
// opacapabilities command bumps it with the file, and NOTICE names it
// (.github/scripts/schema-metadata-notice.sh reads it here).
const opaVersion = "1.21.1"

// opaCapabilities are the capabilities of OPA 1.21.1, the release
// conftest 0.71.1 is built with, as OPA publishes them
// (capabilities/v1.21.1.json in its repository): its built-in
// functions, keywords and features. The file is copied unchanged and is
// OPA's, under Apache-2.0, as REUSE.toml and NOTICE say.
//
//go:embed conftest_capabilities.json
var opaCapabilities []byte

// deniedBuiltins are the built-in functions policies can't call, each
// because it reaches the network, where a policy could send the
// configuration it reads, or reads what conftest wasn't given, such as
// ~/.aws/credentials, which a policy could put into a finding's message
// and so into report.md. OPA's are left out of its capabilities;
// conftest's own are never added to them. The others read only their
// arguments and the configuration: print and trace write to conftest's
// output, and io.jwt.decode_verify and the crypto.x509 functions verify
// against the keys and certificates they are passed, fetching none.
var deniedBuiltins = []string{
	// Sends HTTP requests, and reads the CA certificates and client keys
	// it is told to, from files or environment variables.
	"http.send",
	// Resolves a host name through DNS, which a policy could encode the
	// configuration into.
	"net.lookup_ip_addr",
	// Load the $ref of a JSON schema they are given, over HTTP or from a
	// file.
	"json.match_schema",
	"json.verify_schema",
	// Returns OPA's runtime environment, its environment variables among
	// them.
	"opa.runtime",
	// conftest's: parse_config_file and parse_combined_config_files read
	// the files they name, and parse_config's jsonnet parser reads those
	// an importstr names.
	"parse_config",
	"parse_config_file",
	"parse_combined_config_files",
}

// conftestCapabilities returns the capabilities conftest runs policies
// with (--capabilities): OPA's, but for deniedBuiltins, without
// conftest's own built-in functions, and with no host to reach
// (allow_net), so that a policy that calls a denied function doesn't
// compile. A conftest built with a later OPA runs policies with the
// built-in functions of OPA 1.21.1 all the same: a policy that calls a
// newer one doesn't compile either. They are worked out once, on first use.
var conftestCapabilities = sync.OnceValues(func() ([]byte, error) {
	var capabilities map[string]json.RawMessage
	var builtins []json.RawMessage
	if err := json.Unmarshal(opaCapabilities, &capabilities); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilities["builtins"], &builtins); err != nil {
		return nil, err
	}
	builtins = slices.DeleteFunc(builtins, func(b json.RawMessage) bool {
		var named struct {
			Name string `json:"name"`
		}
		return json.Unmarshal(b, &named) != nil || slices.Contains(deniedBuiltins, named.Name)
	})
	var err error
	if capabilities["builtins"], err = json.Marshal(builtins); err != nil {
		return nil, err
	}
	capabilities["allow_net"] = json.RawMessage("[]")
	return json.Marshal(capabilities)
})
