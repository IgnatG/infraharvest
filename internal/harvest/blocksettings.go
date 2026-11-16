// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// BlockSettings are the arguments, and the nested blocks with theirs, that
// infraharvest writes a block of a Terraform provider with: its own
// provider block, or that of a provider its curated modules use. The MCP
// server's drift tools run Terraform only on roots whose provider blocks
// set nothing else (see mcpserver.Providers.Settings).
type BlockSettings struct {
	Arguments []string
	Blocks    map[string]BlockSettings
}
