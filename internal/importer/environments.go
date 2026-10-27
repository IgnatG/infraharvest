// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sharedEnv is the environment of what no environment tag places, such as
// a network the environments share.
const sharedEnv = config.SharedEnvironment

// defaultEnvTags are the tag keys that name a resource's environment, for
// {env} in a layout without --env-tags.
var defaultEnvTags = []string{"Environment", "environment", "Env", "env"}

// envLayout is DefaultLayout with a root per environment.
const envLayout = "{out}/envs/{env}/{provider}/{account}/{region}/"

// withEnvLayout returns the layout of the roots when environments
// split them: pattern if it has {env}; the default layout per environment
// if pattern is the default and split, as when --env-tags names
// environment tags or the configuration file's environments place the
// resources; else pattern, with no split.
func withEnvLayout(pattern string, split bool) string {
	switch {
	case strings.Contains(pattern, "{env}"):
		return pattern
	case split && (pattern == "" || pattern == DefaultLayout):
		return envLayout
	}
	return pattern
}

// checkEnvironmentFlags rejects the flags the configuration file f's
// environments can't go with, as the command's flags have them once f
// applies: --default-env, and those config.File.CheckEnvironmentFlags
// rejects.
func checkEnvironmentFlags(f *config.File, flags *pflag.FlagSet) error {
	layout := ""
	if flag := flags.Lookup("layout"); flag != nil {
		layout = flag.Value.String()
	}
	var envTags []string
	if flag := flags.Lookup("env-tags"); flag != nil {
		if values, ok := flag.Value.(pflag.SliceValue); ok {
			envTags = values.GetSlice()
		}
	}
	if flag := flags.Lookup("default-env"); flag != nil && flag.Value.String() != "" && len(f.Environments) > 0 {
		return errEnvironmentsAndDefaultEnv
	}
	return f.CheckEnvironmentFlags(layout, envTags, DefaultLayout)
}

// errEnvironmentsAndDefaultEnv is the error for --default-env with the
// configuration file's environments, whose tags, not --env-tags, place
// resources.
var errEnvironmentsAndDefaultEnv = errors.New("--default-env names the environment of what no --env-tags tag places, but environments: in the configuration file places resources: leave the flag out")

// byTagKeys returns the environment tags give a resource by keys, as
// --env-tags does: the value of the first of keys they have, as a
// directory name (see envName); "" for none.
func byTagKeys(keys []string) func(map[string]string) string {
	return func(tags map[string]string) string {
		for _, k := range keys {
			if v := tags[k]; v != "" {
				return envName(v)
			}
		}
		return ""
	}
}

// byConfiguredTags returns the environment tags give a resource by the
// tags of the environments names of envs: the one whose tags they match
// (see config.Environment.Matches), of which the file's check leaves at
// most one; "" for none.
func byConfiguredTags(envs config.Environments, names []string) func(map[string]string) string {
	return func(tags map[string]string) string {
		for _, name := range names {
			if envs[name].Matches(tags) {
				return name
			}
		}
		return ""
	}
}

// configuredEnvOf returns the environment of each resource of a scope
// whose environments, of envs, are names (see config.Environments.For):
// the one environment, if there is one; else the one whose tags its tags
// match, as environments places them, with sharedEnv for what none
// places.
func configuredEnvOf(envs config.Environments, names []string, resourcesByService map[string][]harvest.Resource, tags map[string]map[string]string, parents map[string]string, importID func(harvest.Resource) (string, bool)) func(harvest.Resource) string {
	if len(names) == 1 {
		name := names[0]
		return func(harvest.Resource) string { return name }
	}
	placed := environments(resourcesByService, tags, byConfiguredTags(envs, names), parents, importID, sharedEnv)
	return func(r harvest.Resource) string { return placed[tagKey(r)] }
}

// scopeAccount returns the account, subscription or project of a scope
// as discoveryScope names it (aws/123456789012/eu-west-2), or "" if it
// names none.
func scopeAccount(scope string) string {
	parts := strings.SplitN(scope, "/", 3)
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

// warnUnscoped warns, in the log and the report, when provider can't say
// which account, subscription or project it lists in (see
// discoveryScope), but the configuration file's environments name some
// of its: none of those can then be the environment of what it lists.
func (r *Run) warnUnscoped(provider harvest.Provider, scope string) {
	if scopeAccount(scope) != "" {
		return
	}
	dir := providerDir(provider)
	var named []string
	for _, name := range slices.Sorted(maps.Keys(r.environments)) {
		if len(r.environments[name].Scopes(dir)) > 0 {
			named = append(named, name)
		}
	}
	if len(named) == 0 {
		return
	}
	w := fmt.Sprintf("%s couldn't say which account, subscription or project it lists in, so the configuration file's environments that name one (%s) don't apply to what it lists", provider.Name(), strings.Join(named, ", "))
	if r.report.AddWarning(w) {
		log.Printf("warning: %s", w)
	}
}

// keptRoots keeps resources in the roots earlier imports generated for
// them, where environments would now lay them out elsewhere: a root
// never moves, as its state key comes from its path. With --regenerate,
// such a root hands them over instead, where it can (see split).
type keptRoots struct {
	out string
	// recorded says, by directory, whether a checkpoint records a root
	// there (see checkpointPath).
	recorded map[string]bool
	// kept are the roots resources were kept in, each with the
	// environments of those resources.
	kept map[string][]string
	// splits are the roots whose resources go to the roots of their
	// environments (see split).
	splits map[string]bool
	// shared has where the roots' own states have their resources, if
	// --managed-state reads them (see shared.previousAddressesOf).
	shared *shared
}

func newKeptRoots(out string) *keptRoots {
	return &keptRoots{out: out, recorded: map[string]bool{}, kept: map[string][]string{}, splits: map[string]bool{}}
}

// dirs returns dirOf, the root of each resource with environments, but
// for a resource whose root there no checkpoint records while unsplit,
// its root without environments, names one a checkpoint records and
// split didn't split: that one, which it records as kept, with the
// resource's environment by envOf.
func (k *keptRoots) dirs(dirOf, unsplit func(string, harvest.Resource) string, envOf func(harvest.Resource) string) func(string, harvest.Resource) string {
	return func(service string, r harvest.Resource) string {
		dir := dirOf(service, r)
		before := unsplit(service, r)
		if dir == before || k.isRecorded(dir) || !k.isRecorded(before) || k.splits[filepath.Clean(before)] {
			return dir
		}
		if env := envOf(r); !slices.Contains(k.kept[before], env) {
			k.kept[before] = append(k.kept[before], env)
		}
		return before
	}
}

// split works out which of the roots earlier imports generated, where
// environments, by dirOf, would now lay out the resources of
// resourcesByService that unsplit puts there, give them up to the roots
// of their environments rather than keep them (see dirs). With
// regenerate, as with --regenerate, a root does whose removed blocks can
// hand over what goes (see handOver and planHandovers), and whose every
// resource goes, or that keeps some of the run's: it is then generated
// with removed blocks for them, alone if nothing is left in it, and the
// roots of their environments import them. Without, only a root that has
// nothing left to hand over does, as one an earlier run handed
// everything over from. The other roots keep their resources, and the
// log says why.
func (k *keptRoots) split(resourcesByService map[string][]harvest.Resource, dirOf, unsplit func(string, harvest.Resource) string, importID func(harvest.Resource) (string, bool), regenerate bool) error {
	// Where each resource goes, by the root it would be kept in, or goes
	// anyway, as its environment's root was generated (see dirs), and the
	// roots some of the run's resources stay in.
	going := map[string]map[engine.External]string{}
	staying, kept := map[string]bool{}, map[string]bool{}
	for _, service := range slices.Sorted(maps.Keys(resourcesByService)) {
		for _, r := range resourcesByService[service] {
			dir, before := dirOf(service, r), unsplit(service, r)
			if dir == before {
				staying[filepath.Clean(before)] = true
				continue
			}
			if !k.isRecorded(before) {
				continue
			}
			before = filepath.Clean(before)
			kept[before] = kept[before] || !k.isRecorded(dir)
			if going[before] == nil {
				going[before] = map[engine.External]string{}
			}
			if id, ok := importID(r); ok {
				going[before][engine.External{Type: r.Address.Type, ID: id}] = filepath.Clean(dir)
			}
		}
	}
	for _, before := range sortedDirs(going) {
		if !kept[before] {
			continue // nothing would be kept in it
		}
		root := relativePath(k.out, before)
		c, err := readCheckpoint(k.out, before)
		if err != nil {
			log.Printf("%s: %v; it keeps the resources the environments would put under envs/", root, err)
			continue
		}
		previous, err := k.shared.previousAddressesOf(k.out, before)
		if err != nil {
			return err
		}
		var leaving []departing
		for _, p := range previous {
			if to, ok := going[before][engine.ResourceKey(p.Type, p.ID)]; ok {
				leaving = append(leaving, departing{had: p, to: to})
			}
		}
		sort.Slice(leaving, func(i, j int) bool { return leaving[i].had.Address < leaving[j].had.Address })
		finished := c != nil && c.Result != nil
		var why []string
		switch {
		case !regenerate && (!finished || len(previous) > 0):
			continue // a root never moves (see keptRoots)
		case !finished:
			why = []string{"its import didn't finish"}
		case len(leaving) < len(previous) && !staying[before]:
			why = []string{fmt.Sprintf("the run lists %d of its %d resources, and it hands them over only when every one goes", len(leaving), len(previous))}
		default:
			if _, why, err = handOver(k.out, before, leaving, k.shared); err != nil {
				return err
			}
		}
		if len(why) > 0 {
			log.Printf("%s: it keeps the resources the environments would put under envs/, rather than hand them over (--regenerate): %s", root, strings.Join(why, "; "))
			continue
		}
		if len(previous) > 0 {
			log.Printf("%s: removed blocks hand its resources over to the roots of their environments under envs/ (--regenerate)", root)
		}
		k.splits[before] = true
	}
	return nil
}

// isRecorded reports whether a checkpoint records a root in dir.
func (k *keptRoots) isRecorded(dir string) bool {
	if recorded, ok := k.recorded[dir]; ok {
		return recorded
	}
	recorded, err := fileExists(checkpointPath(k.out, dir))
	if err != nil {
		log.Printf("%s: %v; its root is taken for one no import generated", relativePath(k.out, dir), err)
	}
	k.recorded[dir] = recorded
	return recorded
}

// report returns the kept roots, sorted, each of scope, for the report,
// and logs each.
func (k *keptRoots) report(scope string) []report.KeptRoot {
	dirs := make([]string, 0, len(k.kept))
	for dir := range k.kept {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	kept := make([]report.KeptRoot, 0, len(dirs))
	for _, dir := range dirs {
		envs := slices.Sorted(slices.Values(k.kept[dir]))
		path := relativePath(k.out, dir)
		log.Printf("%s: an earlier import generated this root, so it keeps the resources the environments %s would put under envs/: a root's state key comes from its path", path, strings.Join(envs, ", "))
		kept = append(kept, report.KeptRoot{Path: path, Environments: envs, Scope: scope})
	}
	return kept
}

// environments returns the environment of each resource, by tagKey: the
// one envOfTags gives its tags; for a resource they give none, the
// environment of a resource whose ID its import ID contains, such as a
// bucket's versioning or a subnet's route table association, or of its
// parent, for a child import; else fallback: sharedEnv, or the
// environment --default-env names.
func environments(resourcesByService map[string][]harvest.Resource, tags map[string]map[string]string, envOfTags func(map[string]string) string, parents map[string]string, importID func(harvest.Resource) (string, bool), fallback string) map[string]string {
	envs := map[string]string{}
	byID := map[string]string{}
	var untagged []harvest.Resource
	services := make([]string, 0, len(resourcesByService))
	for service := range resourcesByService {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		for _, r := range resourcesByService[service] {
			key := tagKey(r)
			env := envOfTags(tags[key])
			if env == "" {
				untagged = append(untagged, r)
				continue
			}
			envs[key] = env
			if _, taken := byID[r.Observed.ID]; !taken {
				byID[r.Observed.ID] = env
			}
			if id, ok := importID(r); ok {
				if _, taken := byID[id]; !taken {
					byID[id] = env
				}
			}
		}
	}
	for _, r := range untagged {
		key := tagKey(r)
		if parent, ok := parents[key]; ok && envs[parent] != "" {
			envs[key] = envs[parent]
			continue
		}
		envs[key] = fallback
		id, ok := importID(r)
		if !ok {
			continue
		}
		for _, part := range idParts(id) {
			if env, ok := byID[part]; ok {
				envs[key] = env
				break
			}
		}
	}
	return envs
}

// idSeparators split an import ID into the IDs it is made of, such as
// subnet-1/rtb-2 or sg-1_ingress_tcp_443.
var idSeparators = regexp.MustCompile(`[/_:, |]`)

// idParts returns an import ID and the parts of it.
func idParts(id string) []string {
	return append([]string{id}, idSeparators.Split(id, -1)...)
}

// envNameChars are the characters an environment's directory keeps.
var envNameChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// envName is a tag value as a directory name: lower case, with anything
// but letters, digits, dots, dashes and underscores made a dash.
func envName(value string) string {
	name := strings.Trim(envNameChars.ReplaceAllString(strings.ToLower(value), "-"), "-.")
	if name == "" {
		return sharedEnv
	}
	return name
}

// untaggedEnv returns the environment of what no environment tag places:
// defaultEnv, the value of --default-env, as a directory name (see
// envName), or sharedEnv where it is "".
func untaggedEnv(defaultEnv string) string {
	if defaultEnv == "" {
		return sharedEnv
	}
	return envName(defaultEnv)
}
