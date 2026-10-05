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
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform/providers"
	"github.com/hashicorp/terraform/terraform"
	"github.com/zclconf/go-cty/cty"
)

type Resource struct {
	InstanceInfo  *terraform.InstanceInfo
	InstanceState *terraform.InstanceState
	Outputs       map[string]*terraform.OutputState `json:",omitempty"`
	// ResourceName is the resource's name as a Terraform label (see
	// TfSanitize); RawName is the name as listed, which the Terraform engine
	// labels its own way.
	ResourceName      string
	RawName           string `json:",omitempty"`
	Provider          string
	Item              map[string]interface{} `json:",omitempty"`
	IgnoreKeys        []string               `json:",omitempty"`
	AllowEmptyValues  []string               `json:",omitempty"`
	AdditionalFields  map[string]interface{} `json:",omitempty"`
	SlowQueryRequired bool
	DataFiles         map[string][]byte
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

func (rf *ResourceFilter) Filter(resource Resource) bool {
	if !rf.IsApplicable(strings.TrimPrefix(resource.InstanceInfo.Type, resource.Provider+"_")) {
		return true
	}
	var vals []interface{}
	switch {
	case rf.FieldPath == "id":
		vals = []interface{}{resource.InstanceState.ID}
	case rf.AcceptableValues == nil:
		var hasField = WalkAndCheckField(rf.FieldPath, resource.InstanceState.Attributes)
		if hasField {
			return true
		}
		return WalkAndCheckField(rf.FieldPath, resource.Item)
	default:
		vals = WalkAndGet(rf.FieldPath, resource.InstanceState.Attributes)
		if len(vals) == 0 {
			vals = WalkAndGet(rf.FieldPath, resource.Item)
		}
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

func NewResource(id, resourceName, resourceType, provider string,
	attributes map[string]string,
	allowEmptyValues []string,
	additionalFields map[string]interface{}) Resource {
	return Resource{
		ResourceName: TfSanitize(resourceName),
		RawName:      resourceName,
		Item:         nil,
		Provider:     provider,
		InstanceState: &terraform.InstanceState{
			ID:         id,
			Attributes: attributes,
		},
		InstanceInfo: &terraform.InstanceInfo{
			Type: resourceType,
			Id:   fmt.Sprintf("%s.%s", resourceType, TfSanitize(resourceName)),
		},
		AdditionalFields: additionalFields,
		AllowEmptyValues: allowEmptyValues,
	}
}

func NewSimpleResource(id, resourceName, resourceType, provider string, allowEmptyValues []string) Resource {
	return NewResource(
		id,
		resourceName,
		resourceType,
		provider,
		map[string]string{},
		allowEmptyValues,
		map[string]interface{}{},
	)
}

// StateRefresher reads the live state of a resource from its provider.
type StateRefresher interface {
	Refresh(info *terraform.InstanceInfo, state *terraform.InstanceState) (*terraform.InstanceState, error)
}

// SchemaProvider returns the schema of a provider's resource types.
type SchemaProvider interface {
	GetSchema() *providers.GetSchemaResponse
}

// Refresh replaces the resource's state with its live state. It returns an
// error if the provider could not read the resource or returned no state.
func (r *Resource) Refresh(provider StateRefresher) error {
	if r.SlowQueryRequired {
		time.Sleep(200 * time.Millisecond)
	}
	state, err := provider.Refresh(r.InstanceInfo, r.InstanceState)
	r.InstanceState = state
	if err != nil {
		return err
	}
	if state == nil || state.ID == "" {
		return errors.New("provider returned no state: the resource may have been deleted, or its ID cannot be read")
	}
	return nil
}

func (r Resource) GetIDKey() string {
	if _, exist := r.InstanceState.Attributes["self_link"]; exist {
		return "self_link"
	}
	return "id"
}

func (r *Resource) ParseTFstate(parser Flatmapper, impliedType cty.Type) error {
	attributes, err := parser.Parse(impliedType)
	if err != nil {
		return err
	}

	// add Additional Fields to resource
	for key, value := range r.AdditionalFields {
		attributes[key] = value
	}

	if attributes == nil {
		attributes = map[string]interface{}{} // ensure HCL can represent empty resource correctly
	}

	r.Item = attributes
	return nil
}

func (r *Resource) ConvertTFstate(provider SchemaProvider) error {
	resourceSchema, ok := provider.GetSchema().ResourceTypes[r.InstanceInfo.Type]
	if !ok {
		return fmt.Errorf("provider schema has no resource type %s", r.InstanceInfo.Type)
	}
	ignoreKeys := []*regexp.Regexp{}
	for _, pattern := range r.IgnoreKeys {
		ignoreKeys = append(ignoreKeys, regexp.MustCompile(pattern))
	}
	allowEmptyValues := []*regexp.Regexp{}
	for _, pattern := range r.AllowEmptyValues {
		if pattern != "" {
			allowEmptyValues = append(allowEmptyValues, regexp.MustCompile(pattern))
		}
	}
	parser := NewFlatmapParser(r.InstanceState.Attributes, ignoreKeys, allowEmptyValues)
	return r.ParseTFstate(parser, resourceSchema.Block.ImpliedType())
}

func (r *Resource) ServiceName() string {
	return strings.TrimPrefix(r.InstanceInfo.Type, r.Provider+"_")
}
