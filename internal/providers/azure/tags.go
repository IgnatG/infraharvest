// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

// Every Azure lister records the tags of what it lists from its own
// answers, as tags.<key> attributes (see harvest.AttributeTags):
// resource_graph from the rows Resource Graph returns, and the Resource
// Manager listers from their list responses, which carry each resource's
// tags. So the provider reads no tags of its own, which would take a
// second scan of the subscription in Resource Graph, and fail where
// Resource Graph is missing, the very clouds the Resource Manager
// listers are for.

// tagMap is a resource's tags as Resource Graph returns them, leaving out
// values that aren't strings.
func tagMap(tags interface{}) map[string]string {
	m, _ := tags.(map[string]interface{})
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// armTagAttributes records the tags of a Resource Manager list response's
// resource as Terraform flattens them, tags.<key>, leaving out nil values.
func armTagAttributes(tags map[string]*string) map[string]string {
	attributes := make(map[string]string, len(tags))
	for k, v := range tags {
		if v != nil {
			attributes["tags."+k] = *v
		}
	}
	return attributes
}
