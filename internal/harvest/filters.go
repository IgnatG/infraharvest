// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// idPath is the Path of a filter on the IDs listers record.
const idPath = "id"

// tagPathPrefix starts the Path of a filter on a tag, as --tag writes it.
const tagPathPrefix = "tags."

// Filter is one filter of --id, --tag or --where.
//
// Service limits the filter to one service, named by the Terraform type
// without the provider prefix (vpc, compute_network); empty means every
// service. Path is "id" for a filter on IDs, or else a dotted
// attribute path such as tags.Name. Values are the values that
// pass. For an attribute filter, nil values mean the attribute only has to
// exist, whereas an empty, non-nil slice lets nothing pass.
type Filter struct {
	Service string
	Path    string
	Values  []string
}

// AppliesTo reports whether the filter concerns the service serviceName:
// it does when the filter names no service, or names exactly this one.
func (f *Filter) AppliesTo(serviceName string) bool {
	return f.Service == "" || f.Service == serviceName
}

// Keeps reports whether resource passes the filter. A filter on another
// service never excludes a resource. Otherwise an ID filter passes the
// resources whose recorded ID is one of the values, and an attribute filter
// those whose recorded attribute at Path exists (nil values) or has
// one of the values.
func (f *Filter) Keeps(resource Resource) bool {
	service := strings.TrimPrefix(resource.Address.Type, resource.Provider+"_")
	if !f.AppliesTo(service) {
		return true
	}
	if f.Path == idPath {
		return slices.Contains(f.Values, resource.Observed.ID)
	}
	attributes := resource.Observed.Attributes
	if f.Values == nil {
		return hasPath(f.Path, attributes)
	}
	for _, value := range valuesAt(f.Path, attributes) {
		if slices.Contains(f.Values, reflect.ValueOf(value).String()) {
			return true
		}
	}
	return false
}

// ParseIDFilter parses one --id: <type>=<id>[,<id>...], a filter on the
// IDs the listers record for one type, named without the provider prefix,
// as in vpc=vpc-1,vpc-2. Empty IDs are left out; at least one must remain.
func ParseIDFilter(raw string) (Filter, error) {
	service, ids, ok := strings.Cut(raw, "=")
	if !ok || service == "" {
		return Filter{}, fmt.Errorf("--id %q: use <type>=<id>[,<id>...], such as vpc=vpc-1", raw)
	}
	var values []string
	for _, id := range strings.Split(ids, ",") {
		if id != "" {
			values = append(values, id)
		}
	}
	if len(values) == 0 {
		return Filter{}, fmt.Errorf("--id %q names no ID", raw)
	}
	return Filter{Service: service, Path: idPath, Values: values}, nil
}

// ParseTagFilter parses one --tag: <key>[=<value>[,<value>...]], a filter
// on the tag key, of every type: with values, the tag must have one of
// them; without, the resource only has to have the tag.
func ParseTagFilter(raw string) (Filter, error) {
	f, err := parseAttributeFilter(raw, "key")
	if err != nil {
		return Filter{}, fmt.Errorf("--tag %q: %w; use <key>[=<value>[,<value>...]], such as env=prod", raw, err)
	}
	f.Path = tagPathPrefix + f.Path
	return f, nil
}

// ParseWhereFilter parses one --where: <path>[=<value>[,<value>...]], a
// filter on the attribute at a dotted path, of every type: with values,
// the attribute must have one of them; without, it only has to exist.
func ParseWhereFilter(raw string) (Filter, error) {
	f, err := parseAttributeFilter(raw, "path")
	if err != nil {
		return Filter{}, fmt.Errorf("--where %q: %w; use <path>[=<value>[,<value>...]], such as tags.env=prod", raw, err)
	}
	if f.Path == idPath {
		// A filter on this path is one on IDs, of every type: --id names the
		// type.
		return Filter{}, fmt.Errorf("--where %q: filter on IDs with --id <type>=<id>[,<id>...]", raw)
	}
	return f, nil
}

// parseAttributeFilter parses <path>[=<values>], the values separated by
// commas. what names the path in the error for an empty one.
func parseAttributeFilter(raw, what string) (Filter, error) {
	path, values, hasValues := strings.Cut(raw, "=")
	if path == "" {
		return Filter{}, errors.New("no " + what)
	}
	f := Filter{Path: path}
	if hasValues {
		f.Values = strings.Split(values, ",")
	}
	return f, nil
}
