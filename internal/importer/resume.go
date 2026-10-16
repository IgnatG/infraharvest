// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/version"
)

// CheckpointDir holds, in the output directory, a checkpoint of each root
// an import generated, so that --resume can skip it. .gitignore keeps it
// out of version control.
const CheckpointDir = ".infraharvest"

// checkpoint records a generated root: what it was generated from, and
// the result, for the report.
type checkpoint struct {
	Fingerprint string         `json:"fingerprint"`
	Result      *engine.Result `json:"result"`
}

// fingerprint identifies what a root is generated from: infraharvest's
// version, the engine, the imports, the files written besides them, and
// the options that change the output.
func fingerprint(engineVersion string, imports []engine.Import, opts engine.Options) (string, error) {
	// Imports come in listing order, which varies between runs.
	imports = append([]engine.Import(nil), imports...)
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].Type != imports[j].Type {
			return imports[i].Type < imports[j].Type
		}
		return imports[i].ID < imports[j].ID
	})
	adapters := make([]string, 0, len(opts.Adapters))
	for _, a := range opts.Adapters {
		adapters = append(adapters, a.Source+"@"+a.Version)
	}
	content, err := json.Marshal(struct {
		Version, Engine string
		Imports         []engine.Import
		Config          map[string][]byte
		Omit, Defaults  map[string][]string
		StateOnly       map[string][]string
		KeepEmpty       map[string][]string
		DefaultTags     *engine.DefaultTags
		EffectiveTags   *engine.EffectiveTags
		Inherited       []string
		Modules         bool
		Adapters        []string
		External        []engine.External
		DataSources     map[string]engine.DataSource
		Placeholders    map[string]engine.Placeholder
		Lifecycles      map[string]engine.Lifecycle
		IncludeOwned    bool
		KeepOwned       map[string]bool
		OtherRoots      []engine.RootResource
		ARNs            map[string]engine.ARN
	}{version.Version, engineVersion, imports, opts.Config, opts.Omit, opts.Defaults, opts.StateOnly, opts.KeepEmpty, opts.DefaultTags, opts.EffectiveTags, opts.Inherited, opts.ModulesDir != "", adapters, opts.External, opts.DataSources, opts.Placeholders, opts.Lifecycles, opts.IncludeOwned, opts.KeepOwned, opts.OtherRoots, opts.ARNs})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func checkpointPath(out, dir string) string {
	return filepath.Join(out, CheckpointDir, "checkpoints", filepath.FromSlash(relativePath(out, dir))+".json")
}

// resumed returns the result a checkpoint recorded for dir, if it has the
// same fingerprint and the root's files are still there; nil otherwise.
func resumed(out, dir, fp string) (*engine.Result, error) {
	content, err := os.ReadFile(checkpointPath(out, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c checkpoint
	if err := json.Unmarshal(content, &c); err != nil || c.Fingerprint != fp || c.Result == nil {
		return nil, nil // an unreadable checkpoint means generating again
	}
	for _, name := range append([]string{engine.ImportsFileName, engine.VersionsFileName}, configurationFiles(c.Result)...) {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return nil, nil // a missing file means generating again
		}
	}
	return c.Result, nil
}

// configurationFiles are the files a result's configuration is in: the
// files it was split into, or generated.tf.
func configurationFiles(result *engine.Result) []string {
	if result != nil && len(result.Files) > 0 {
		return result.Files
	}
	return []string{engine.GeneratedFileName}
}

// saveCheckpoint records that dir was generated from fp, with result.
func saveCheckpoint(out, dir, fp string, result *engine.Result) error {
	path := checkpointPath(out, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(checkpoint{Fingerprint: fp, Result: result}, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(content, '\n'), 0o644)
}

// generatedFiles are the files a previous run wrote into a root, which
// --resume removes before generating the root again.
var generatedFiles = []string{
	engine.GeneratedFileName, engine.ImportsFileName, engine.RejectedFileName, engine.LocalsFileName,
	engine.VariablesFileName, engine.DataFileName, engine.ReadmeFileName, BackendFileName,
}

// clearGenerated removes what a previous run generated in dir, so that the
// root can be generated again: generatedFiles, the files its checkpoint
// under out says the configuration was split into, and the files
// --incremental added (see engine.AddedFileName), which the checkpoint
// names too.
func clearGenerated(out, dir string) error {
	names := slices.Clone(generatedFiles)
	if content, err := os.ReadFile(checkpointPath(out, dir)); err == nil {
		var c checkpoint
		if json.Unmarshal(content, &c) == nil && c.Result != nil {
			names = append(names, c.Result.Files...)
		}
	}
	for _, name := range names {
		if filepath.Base(name) != name {
			continue // a checkpoint names files of the root only
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	added, err := filepath.Glob(filepath.Join(dir, "generated_*.tf"))
	if err != nil {
		return err
	}
	for _, path := range added {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// claimRoot gets dir ready for generating a root from fp. A directory no
// checkpoint under out says an import wrote must hold no .tf files, so
// that an unrelated root is never overwritten. With resume, what a
// previous run generated in dir is removed (see clearGenerated). Last, a
// checkpoint without a result records that the root's generation started,
// so that a later run knows the files are infraharvest's even if this one
// fails.
func claimRoot(out, dir, fp string, resume bool) error {
	owned, err := fileExists(checkpointPath(out, dir))
	if err != nil {
		return err
	}
	switch {
	case owned && !resume:
		// Generate refuses a root an earlier run left; its checkpoint stays
		// as it is.
		return nil
	case owned:
		if err := clearGenerated(out, dir); err != nil {
			return err
		}
	default:
		paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
		if err != nil {
			return err
		}
		if len(paths) > 0 {
			names := make([]string, 0, len(paths))
			for _, path := range paths {
				names = append(names, filepath.Base(path))
			}
			return fmt.Errorf("%s already holds %s, which no import by infraharvest wrote: use an empty output directory, or --incremental to add to a root an import generated", dir, strings.Join(names, ", "))
		}
	}
	return saveCheckpoint(out, dir, fp, nil)
}

// fileExists reports whether path exists.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
