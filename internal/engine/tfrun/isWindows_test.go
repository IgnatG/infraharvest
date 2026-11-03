// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import "runtime"

func isWindows() bool { return runtime.GOOS == "windows" }
