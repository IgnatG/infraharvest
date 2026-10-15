// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// Address identifies a listed resource in the generated configuration.
// Type is the Terraform resource type, such as aws_sqs_queue. ID is the
// resource's Terraform address, "<type>.<label>", and not its cloud ID.
type Address struct {
	Type string
	ID   string
}

// Observation is what a lister recorded about a resource. ID is the ID it
// found, normally the import ID, though the catalogue may derive another.
// Attributes are flattened the way Terraform flattens state (tags.Name,
// labels.env, tags.%), for filters and for mapping import IDs.
type Observation struct {
	ID         string            `json:"id"`
	Attributes map[string]string `json:"attributes"`
}

// Resource is one resource a lister listed. Label is its Terraform label
// (see the Label function) and RawName the name as listed, from which the
// engine makes labels of its own. Provider is the provider's short name.
type Resource struct {
	Address  *Address
	Observed *Observation
	Label    string
	RawName  string `json:",omitempty"`
	Provider string
}

// Record returns a resource of type resourceType, listed by provider, with
// the cloud ID id and the name resourceName. Its label is
// Label(resourceName) and its address "<resourceType>.<label>".
//
// The resource keeps attributes itself rather than a copy, so a lister may
// go on filling the map; a nil map stays nil. Nothing is validated: empty
// strings are accepted, and the type need not belong to the provider.
func Record(id, resourceName, resourceType, provider string, attributes map[string]string) Resource {
	label := Label(resourceName)
	return Resource{
		Address: &Address{
			Type: resourceType,
			ID:   resourceType + "." + label,
		},
		Observed: &Observation{
			ID:         id,
			Attributes: attributes,
		},
		Label:    label,
		RawName:  resourceName,
		Provider: provider,
	}
}

// RecordBare is Record for a lister that records no attributes. The
// resource gets an empty attribute map of its own, so attributes can be
// added to it later without touching any other resource.
func RecordBare(id, resourceName, resourceType, provider string) Resource {
	return Record(id, resourceName, resourceType, provider, map[string]string{})
}
