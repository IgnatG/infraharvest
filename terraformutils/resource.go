// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package terraformutils

import (
	"fmt"
	"regexp"
	"strings"
)

// InstanceInfo identifies a listed resource.
type InstanceInfo struct {
	// Type is the Terraform resource type, such as aws_sqs_queue.
	Type string
	// Id is the resource's address, <type>.<label>.
	Id string
}

// ResourceAddress returns the resource's address, <type>.<label>, where the
// label is the part of Id after the type and a dot.
func (i *InstanceInfo) ResourceAddress() string {
	return i.Type + "." + strings.TrimPrefix(i.Id, i.Type+".")
}

// InstanceState is what a lister recorded about a resource.
type InstanceState struct {
	// ID is the ID the lister found: the import ID, unless the provider
	// maps it (see ProviderWithImportIDs).
	ID string `json:"id"`
	// Attributes are the attributes the lister found, for filters and for
	// mapping to import IDs.
	Attributes map[string]string `json:"attributes"`
}

type Resource struct {
	InstanceInfo  *InstanceInfo
	InstanceState *InstanceState
	// ResourceName is the resource's name as a Terraform label (see
	// TfSanitize); RawName is the name as listed, which the Terraform engine
	// labels its own way.
	ResourceName string
	RawName      string `json:",omitempty"`
	Provider     string
}

type ApplicableFilter interface {
	IsApplicable(resourceName string) bool
}

type ResourceFilter struct {
	ApplicableFilter
	ServiceName      string
	FieldPath        string
	AcceptableValues []string
}

// Filter reports whether resource passes the filter: its ID, or the
// attributes its lister recorded, match.
func (rf *ResourceFilter) Filter(resource Resource) bool {
	if !rf.IsApplicable(strings.TrimPrefix(resource.InstanceInfo.Type, resource.Provider+"_")) {
		return true
	}
	var vals []interface{}
	switch {
	case rf.FieldPath == "id":
		vals = []interface{}{resource.InstanceState.ID}
	case rf.AcceptableValues == nil:
		return WalkAndCheckField(rf.FieldPath, resource.InstanceState.Attributes)
	default:
		vals = WalkAndGet(rf.FieldPath, resource.InstanceState.Attributes)
	}
	for _, val := range vals {
		for _, acceptableValue := range rf.AcceptableValues {
			if val == acceptableValue {
				return true
			}
		}
	}
	return false
}

func (rf *ResourceFilter) IsApplicable(serviceName string) bool {
	return rf.ServiceName == "" || rf.ServiceName == serviceName
}

func (rf *ResourceFilter) isInitial() bool {
	return rf.FieldPath == "id"
}

// NewResource records a listed resource: its ID, its name, its Terraform
// type, its provider and the attributes the lister found.
func NewResource(id, resourceName, resourceType, provider string, attributes map[string]string) Resource {
	return Resource{
		ResourceName: TfSanitize(resourceName),
		RawName:      resourceName,
		Provider:     provider,
		InstanceState: &InstanceState{
			ID:         id,
			Attributes: attributes,
		},
		InstanceInfo: &InstanceInfo{
			Type: resourceType,
			Id:   fmt.Sprintf("%s.%s", resourceType, TfSanitize(resourceName)),
		},
	}
}

// NewSimpleResource records a listed resource without attributes.
func NewSimpleResource(id, resourceName, resourceType, provider string) Resource {
	return NewResource(id, resourceName, resourceType, provider, map[string]string{})
}

var unsafeChars = regexp.MustCompile(`[^0-9A-Za-z_\-]`)

func escapeRune(s string) string {
	return fmt.Sprintf("-%04X-", s)
}

// TfSanitize makes name a Terraform label.
func TfSanitize(name string) string {
	name = unsafeChars.ReplaceAllStringFunc(name, escapeRune)
	name = "tfer--" + name
	return name
}
