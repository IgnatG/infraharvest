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

func ParseFilterValues(value string) []string {
	var values []string

	valueBuffering := true
	wrapped := false
	var valueBuffer []byte
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' {
			wrapped = !wrapped
			continue
		} else if value[i] == ':' {
			if len(valueBuffer) == 0 {
				continue
			} else if valueBuffering && !wrapped {
				values = append(values, string(valueBuffer))
				valueBuffering = false
				valueBuffer = []byte{}
				continue
			}
		}
		valueBuffering = true
		valueBuffer = append(valueBuffer, value[i])
	}
	if len(valueBuffer) > 0 {
		values = append(values, string(valueBuffer))
	}

	return values
}

// FilterCleanup keeps the resources of s that pass its ID filters, without
// duplicates. Filters on other attributes are left to the listers that
// pass them to the API they list with (see ResourceFilter.Filter).
func FilterCleanup(s *Service) {
	if len(s.Filter) == 0 {
		return
	}
	var newListOfResources []Resource
	seen := map[string]struct{}{}
	for _, resource := range s.Resources {
		allPredicatesTrue := true
		for _, filter := range s.Filter {
			if filter.FieldPath == "id" {
				allPredicatesTrue = allPredicatesTrue && filter.Filter(resource)
			}
		}
		// Resources of different types can share an ID, such as a role and
		// a group of the same name, so the type is part of the key.
		key := resource.InstanceInfo.Type + "\x00" + resource.InstanceInfo.ID
		if _, duplicate := seen[key]; allPredicatesTrue && !duplicate {
			seen[key] = struct{}{}
			newListOfResources = append(newListOfResources, resource)
		}
	}
	s.Resources = newListOfResources
}
