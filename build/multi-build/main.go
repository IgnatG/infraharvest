// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Command multi-build builds one infraharvest binary per provider for each
// release platform, using the `minimal,<provider>` build tags. Run it from
// the repository root.
package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var platforms = []struct{ goos, goarch, suffix string }{
	{"linux", "amd64", ""},
	{"linux", "arm64", ""},
	{"windows", "amd64", ".exe"},
	{"windows", "arm64", ".exe"},
	{"darwin", "amd64", ""},
	{"darwin", "arm64", ""},
}

func main() {
	files, err := filepath.Glob(filepath.Join("cmd", "provider_cmd_*.go"))
	if err != nil || len(files) == 0 {
		log.Fatalf("no provider files found in cmd/ (run from the repository root): %v", err)
	}
	for _, file := range files {
		provider := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "provider_cmd_"), ".go")
		for _, p := range platforms {
			binary := "infraharvest-" + provider + "-" + p.goos + "-" + p.goarch + p.suffix
			log.Printf("building %s", binary)
			cmd := exec.Command("go", "build", "-trimpath", "-tags", "minimal,"+provider, "-o", binary, ".")
			cmd.Env = append(os.Environ(), "GOOS="+p.goos, "GOARCH="+p.goarch)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				log.Fatalf("build %s: %v", binary, err)
			}
		}
	}
}
