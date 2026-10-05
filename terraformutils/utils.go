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
	"bytes"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/IgnatG/infraharvest/terraformutils/providerwrapper"

	"github.com/hashicorp/terraform/terraform"
)

type BaseResource struct {
	Tags map[string]string `json:"tags,omitempty"`
}

func NewTfState(resources []Resource) *terraform.State {
	tfstate := &terraform.State{
		Version:   terraform.StateVersion,
		TFVersion: terraform.VersionString(), //nolint
		Serial:    1,
	}
	outputs := map[string]*terraform.OutputState{}
	for _, r := range resources {
		for k, v := range r.Outputs {
			outputs[k] = v
		}
	}
	tfstate.Modules = []*terraform.ModuleState{
		{
			Path:      []string{"root"},
			Resources: map[string]*terraform.ResourceState{},
			Outputs:   outputs,
		},
	}
	for _, resource := range resources {
		resourceState := &terraform.ResourceState{
			Type:     resource.InstanceInfo.Type,
			Primary:  resource.InstanceState,
			Provider: "provider." + resource.Provider,
		}
		tfstate.Modules[0].Resources[resource.InstanceInfo.Type+"."+resource.ResourceName] = resourceState
	}
	return tfstate
}

func PrintTfState(resources []Resource) ([]byte, error) {
	state := NewTfState(resources)
	var buf bytes.Buffer
	err := terraform.WriteState(state, &buf)
	return buf.Bytes(), err
}

// RefreshResources refreshes resources in parallel and returns the ones that
// refreshed. Each resource that could not be refreshed is left out and
// reported in failures, sorted for stable output.
func RefreshResources(resources []*Resource, provider StateRefresher, slowProcessingResources [][]*Resource) (refreshedResources []*Resource, failures []error) {
	total := len(resources)
	for _, resourceGroup := range slowProcessingResources {
		total += len(resourceGroup)
	}
	errs := make(chan error, total)
	input := make(chan *Resource, len(resources))
	var wg sync.WaitGroup
	poolSize := 15
	for i := range resources {
		wg.Add(1)
		input <- resources[i]
	}
	close(input)

	for i := 0; i < poolSize; i++ {
		go RefreshResourceWorker(input, &wg, provider, errs)
	}

	spInputs := []chan *Resource{}
	for i, resourceGroup := range slowProcessingResources {
		spInputs = append(spInputs, make(chan *Resource, len(resourceGroup)))
		for j := range resourceGroup {
			spInputs[i] <- resourceGroup[j]
		}
		close(spInputs[i])
	}

	for i := 0; i < len(spInputs); i++ {
		wg.Add(len(slowProcessingResources[i]))
		go RefreshResourceWorker(spInputs[i], &wg, provider, errs)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		failures = append(failures, err)
	}
	sortErrors(failures)

	for _, r := range resources {
		if r.InstanceState != nil && r.InstanceState.ID != "" {
			refreshedResources = append(refreshedResources, r)
		}
	}
	for _, resourceGroup := range slowProcessingResources {
		for _, r := range resourceGroup {
			if r.InstanceState != nil && r.InstanceState.ID != "" {
				refreshedResources = append(refreshedResources, r)
			}
		}
	}
	return refreshedResources, failures
}

// RefreshResourcesByProvider refreshes every resource in the mapping and
// keeps the ones that refreshed. It returns one error per resource dropped.
func RefreshResourcesByProvider(providersMapping *ProvidersMapping, refresher StateRefresher) []error {
	allResources := providersMapping.ShuffleResources()
	slowProcessingResources := make(map[ProviderGenerator][]*Resource)
	regularResources := []*Resource{}
	for i := range allResources {
		resource := allResources[i]
		if resource.SlowQueryRequired {
			provider := providersMapping.MatchProvider(resource)
			if slowProcessingResources[provider] == nil {
				slowProcessingResources[provider] = []*Resource{}
			}
			slowProcessingResources[provider] = append(slowProcessingResources[provider], resource)
		} else {
			regularResources = append(regularResources, resource)
		}
	}

	var spResourcesList [][]*Resource
	for p := range slowProcessingResources {
		spResourcesList = append(spResourcesList, slowProcessingResources[p])
	}

	refreshedResources, failures := RefreshResources(regularResources, refresher, spResourcesList)
	providersMapping.SetResources(refreshedResources)
	return failures
}

func RefreshResourceWorker(input <-chan *Resource, wg *sync.WaitGroup, provider StateRefresher, errs chan<- error) {
	for r := range input {
		log.Println("Refreshing state...", r.InstanceInfo.Id)
		if err := r.Refresh(provider); err != nil {
			errs <- fmt.Errorf("refresh %s: %w", r.InstanceInfo.Id, err)
		}
		wg.Done()
	}
}

// sortErrors orders errors by message so that reports are deterministic.
func sortErrors(errs []error) {
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
}

func IgnoreKeys(resourcesTypes []string, p *providerwrapper.ProviderWrapper) map[string][]string {
	readOnlyAttributes, err := p.GetReadOnlyAttributes(resourcesTypes)
	if err != nil {
		log.Println("plugin error 2:", err)
		return map[string][]string{}
	}
	return readOnlyAttributes
}

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

func FilterCleanup(s *Service, isInitial bool) {
	if len(s.Filter) == 0 {
		return
	}
	var newListOfResources []Resource
	seen := map[string]struct{}{}
	for _, resource := range s.Resources {
		allPredicatesTrue := true
		for _, filter := range s.Filter {
			if filter.isInitial() == isInitial {
				allPredicatesTrue = allPredicatesTrue && filter.Filter(resource)
			}
		}
		// Resources of different types can share an ID, such as a role and
		// a group of the same name, so the type is part of the key.
		key := resource.InstanceInfo.Type + "\x00" + resource.InstanceInfo.Id
		if _, duplicate := seen[key]; allPredicatesTrue && !duplicate {
			seen[key] = struct{}{}
			newListOfResources = append(newListOfResources, resource)
		}
	}
	s.Resources = newListOfResources
}
