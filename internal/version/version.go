// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package version holds the version infraharvest reports, which release
// builds set at link time.
package version

import "runtime/debug"

// Version is infraharvest's version, as the CLI, the MCP server, reports and
// resume fingerprints give it.
//
// Release builds set it with
//
//	-ldflags "-X github.com/IgnatG/infraharvest/internal/version.Version=v1.2.3"
//
// which only works on a package-level string variable whose initial value
// is a constant, so it must stay one. A build that sets none takes the main
// module's version from the build information, see buildVersion.
var Version = devVersion

// devVersion is the version of a build that sets none and records none. It
// also tells buildVersion that the linker left Version alone.
const devVersion = "v0.1.0-dev"

// init adopts the version the Go build information records, as go install
// module@version and builds in a git checkout record one. The linker's
// value is already in Version by now, and wins.
func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		Version = buildVersion(Version, info.Main.Version)
	}
}

// buildVersion returns the version a build reports: recorded, the main
// module's version from the build information, when set is still devVersion
// and recorded is a real version (neither empty nor "(devel)", which go
// test and builds without version control information record), and set
// otherwise. Neither string is checked or changed.
func buildVersion(set, recorded string) string {
	if set == devVersion && recorded != "" && recorded != "(devel)" {
		return recorded
	}
	return set
}
