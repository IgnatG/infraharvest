// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// rootFiles are the files every output directory starts with, and the
// versions they pin.
type rootFiles struct {
	files         map[string][]byte // versions.tf and providers.tf
	engineVersion string
	provider      engine.Provider // Version is the constraint
	// version is the provider version the constraint pins the release
	// line of, which decides the catalogue entries that depend on it.
	version *goversion.Version
	// heldBack says which modules keep the provider below its newest
	// release, within its newest major release, if any do.
	heldBack string
	// refused are the module versions that accept only an older major
	// release of the provider: their adapters aren't used.
	refused []moduleConstraint
}

// rootConfig renders versions.tf and providers.tf for every output
// directory: required_version from the oldest Terraform release generated
// configuration works with (engine.MinimumVersion; Generate raises it for
// what a root uses), and the provider pinned to its newest minor release
// line in registry (the engine's default registry), unless its source names
// another: the newest release the module versions the import may call
// accept (constraints). A module version that accepts no release of the
// provider's newest major release line doesn't hold the provider back: it
// is refused, and the import doesn't call it (see newestMajor). A
// registryURL replaces the registry, for tests.
func rootConfig(ctx context.Context, client *http.Client, registry, registryURL, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	engineVersion, err := tfrun.BinaryVersion(ctx, execPath)
	if err != nil {
		return nil, err
	}
	source := qualifiedSource(registry, p.Source)
	latest, err := engine.LatestProviderVersion(ctx, client, registryURL, source)
	if err != nil {
		return nil, err
	}
	constraints, refused := newestMajor(ctx, client, registryURL, source, latest, constraints)
	for _, c := range refused {
		log.Printf("%s %s: not called: it accepts %s %s, and the newest release is %s; its resources stay in the root", c.module, c.version, p.Source, c.constraint, latest)
	}
	chosen, held := latest, ""
	if len(constraints) > 0 {
		all := make([]goversion.Constraints, len(constraints))
		for i, c := range constraints {
			all[i] = c.constraint
		}
		v, err := engine.ProviderVersion(ctx, client, registryURL, source, all)
		switch {
		case err != nil:
			// The modules' calls then fail their plans and are declined.
			log.Printf("%s: %v; pinning the newest release, %s", p.Source, err, latest)
		case v.LessThan(latest):
			chosen, held = v, heldBack(latest, constraints)
			log.Printf("%s: pinning %s: %s", p.Source, v, held)
		}
	}
	p.Version = engine.ProviderConstraint(chosen)
	providers, err := engine.ProvidersFile(p)
	if err != nil {
		return nil, err
	}
	return &rootFiles{
		files: map[string][]byte{
			engine.VersionsFileName:  engine.VersionsFile(engine.RequiredVersion(engine.MinimumVersion()), p),
			engine.ProvidersFileName: providers,
		},
		engineVersion: engineVersion.String(),
		provider:      p,
		heldBack:      held,
		refused:       refused,
		version:       chosen,
	}, nil
}

// writeGitignore writes a .gitignore for Terraform into the output
// directory, unless it has one.
func writeGitignore(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outputDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	// Atomically: accounts imported in parallel write it at the same time.
	return fsutil.WriteFile(path, []byte(engine.GitignoreFile), 0o644)
}

// engineProvider describes the provider for the generated configuration.
func engineProvider(provider harvest.Provider) engine.Provider {
	name := provider.Name()
	source := provider.Profile().Source
	if source == "" {
		source = "hashicorp/" + name
	}
	var config map[string]interface{}
	if providers, ok := provider.ProviderBlock()["provider"].(map[string]interface{}); ok {
		config, _ = providers[name].(map[string]interface{})
	}
	return engine.Provider{Name: name, Source: source, Config: config}
}

// qualifiedSource adds registry to a provider source without a host:
// hashicorp/aws resolves to registry.terraform.io/hashicorp/aws in
// Terraform and registry.opentofu.org/hashicorp/aws in OpenTofu.
func qualifiedSource(registry, source string) string {
	if strings.Count(source, "/") == 1 {
		return registry + "/" + source
	}
	return source
}

// BackendFileName holds a generated root's state backend.
const BackendFileName = "backend.tf"
