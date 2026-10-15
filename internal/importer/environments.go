// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"regexp"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// sharedEnv is the environment of what no environment tag places, such as
// a network the environments share.
const sharedEnv = "shared"

// defaultEnvTags are the tag keys that name a resource's environment, for
// {env} in a layout without --env-tags.
var defaultEnvTags = []string{"Environment", "environment", "Env", "env"}

// envLayout is DefaultLayout with a root per environment.
const envLayout = "{out}/envs/{env}/{provider}/{account}/{region}/"

// withEnvLayout returns the layout of the roots when environments
// split them: pattern if it has {env}; the default layout per environment
// if pattern is the default and envTags name environment tags; else
// pattern, with no split.
func withEnvLayout(pattern string, envTags []string) string {
	switch {
	case strings.Contains(pattern, "{env}"):
		return pattern
	case len(envTags) > 0 && (pattern == "" || pattern == DefaultLayout):
		return envLayout
	}
	return pattern
}

// environments returns the environment of each resource, by tagKey: the
// value of the first of keys its tags have; for a resource without one,
// the environment of a resource whose ID its import ID contains, such as
// a bucket's versioning or a subnet's route table association, or of its
// parent, for a child import; else sharedEnv.
func environments(resourcesByService map[string][]harvest.Resource, tags map[string]map[string]string, keys []string, parents map[string]string, importID func(harvest.Resource) (string, bool)) map[string]string {
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
			env := ""
			for _, k := range keys {
				if v := tags[key][k]; v != "" {
					env = envName(v)
					break
				}
			}
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
		envs[key] = sharedEnv
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
