// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"fmt"
	"log"
	"reflect"
)

// Registry gives every service an import lists a provider of its own, so
// that services can be listed at the same time: a provider has one current
// service, which Select sets, and so cannot list two at once.
//
// The importer uses it in three steps, from one goroutine: it registers
// every service with Register and lists each with the provider it got
// back; it then drops the services that failed with Unregister and gathers
// what the others listed with Collect; finally it reads the resources by
// service with ByService. An unregistered service's provider is never read
// again, so a listing of it that is still running in the background is
// left alone.
//
// The exported fields are sets, whose values are always true. A Registry
// is not safe for concurrent use.
type Registry struct {
	// Resources holds every resource Collect collected, each its own entry
	// even when two are equal.
	Resources map[*Resource]bool
	// Services holds the names of the registered services.
	Services map[string]bool
	// Providers holds the registered per-service providers. The base
	// provider is never one of them.
	Providers map[Provider]bool

	// baseProvider is the provider whose concrete type the per-service
	// providers have.
	baseProvider Provider
	// serviceOf is the service each registered provider lists.
	serviceOf map[Provider]string
	// providerOf is the newest provider registered for each service.
	providerOf map[string]Provider
	// listedBy is the provider each collected resource came from.
	listedBy map[*Resource]Provider
	// collected is Resources in the order Collect collected them, which
	// keeps each lister's order.
	collected []*Resource
}

// NewRegistry returns an empty registry whose per-service providers will
// have the concrete type of baseProvider. baseProvider is only kept for its
// type: it is neither called nor registered.
func NewRegistry(baseProvider Provider) *Registry {
	return &Registry{
		Resources:    make(map[*Resource]bool),
		Services:     make(map[string]bool),
		Providers:    make(map[Provider]bool),
		baseProvider: baseProvider,
		serviceOf:    make(map[Provider]string),
		providerOf:   make(map[string]Provider),
		listedBy:     make(map[*Resource]Provider),
	}
}

// Register registers service with a new provider of its own and returns
// that provider. The provider is the zero value of the base provider's
// type: nothing is copied from the base provider, and neither Init nor
// Select has been called on it, which is the caller's job.
//
// Registering a service twice registers two providers. Both stay among
// Providers and both list for that service, but Unregister only knows the
// newer one.
func (p *Registry) Register(service string) Provider {
	provider := p.newProvider()
	p.Providers[provider] = true
	p.Services[service] = true
	p.serviceOf[provider] = service
	p.providerOf[service] = provider
	return provider
}

// newProvider returns a pointer to a new zero value of the type the base
// provider points to. It panics when the base provider is nil or not a
// pointer, which Provider implementations must be.
func (p *Registry) newProvider() Provider {
	baseType := reflect.TypeOf(p.baseProvider)
	if baseType == nil || baseType.Kind() != reflect.Pointer {
		panic(fmt.Sprintf("harvest: the base provider must be a non-nil pointer, not %v", baseType))
	}
	return reflect.New(baseType.Elem()).Interface().(Provider)
}

// Unregister unregisters each of services together with its provider (the
// newest, for a service registered twice), so that Collect leaves them
// out. Names that are not registered are ignored. Resources already
// collected stay.
func (p *Registry) Unregister(services []string) {
	for _, service := range services {
		delete(p.Services, service)
		if provider, ok := p.providerOf[service]; ok {
			delete(p.Providers, provider)
			delete(p.serviceOf, provider)
		}
		delete(p.providerOf, service)
	}
}

// Collect collects the resources of every registered provider's selected
// lister into Resources, as copies of the listed values (their Address and
// Observed are shared with the lister), and logs how many each service
// listed. The providers are visited in no fixed order.
//
// Every registered provider must have a selected lister, so the services
// whose Select or listing failed must be unregistered first. Calling it
// again collects everything a second time.
func (p *Registry) Collect() {
	for provider := range p.Providers {
		lister := provider.Selected()
		resources := lister.Listed()
		log.Printf("%s: %d resources listed", lister.Name(), len(resources))
		for _, resource := range resources {
			// resource is a new variable, so a new copy, each iteration.
			collected := &resource
			p.Resources[collected] = true
			p.listedBy[collected] = provider
			p.collected = append(p.collected, collected)
		}
	}
}

// ByService returns copies of the collected resources by the name of the
// service that listed them, each service's in the order its lister gave
// them. Every registered service has an entry, an empty slice when it
// listed nothing. The registry is left as it is, so every call returns the
// same.
//
// The older provider of a service registered twice, then unregistered, is
// still registered, and its resources still appear under the service's
// name.
func (p *Registry) ByService() map[string][]Resource {
	byService := make(map[string][]Resource, len(p.Services))
	for service := range p.Services {
		byService[service] = []Resource{}
	}
	for _, resource := range p.collected {
		if !p.Resources[resource] {
			continue
		}
		service := p.serviceOf[p.listedBy[resource]]
		byService[service] = append(byService[service], *resource)
	}
	return byService
}
