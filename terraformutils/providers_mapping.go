package terraformutils

import (
	"log"
	"reflect"
)

// ProvidersMapping lists services each with a provider of their own, made
// from one base provider, and keeps which resources each one listed.
type ProvidersMapping struct {
	baseProvider       ProviderGenerator
	Resources          map[*Resource]bool
	Services           map[string]bool
	Providers          map[ProviderGenerator]bool
	providerToService  map[ProviderGenerator]string
	serviceToProvider  map[string]ProviderGenerator
	resourceToProvider map[*Resource]ProviderGenerator
}

func NewProvidersMapping(baseProvider ProviderGenerator) *ProvidersMapping {
	providersMapping := &ProvidersMapping{
		baseProvider:       baseProvider,
		Resources:          map[*Resource]bool{},
		Services:           map[string]bool{},
		Providers:          map[ProviderGenerator]bool{},
		providerToService:  map[ProviderGenerator]string{},
		serviceToProvider:  map[string]ProviderGenerator{},
		resourceToProvider: map[*Resource]ProviderGenerator{},
	}

	return providersMapping
}

func deepCopyProvider(provider ProviderGenerator) ProviderGenerator {
	return reflect.New(reflect.ValueOf(provider).Elem().Type()).Interface().(ProviderGenerator)
}

// AddServiceToProvider returns a new provider, of the base provider's type,
// for service.
func (p *ProvidersMapping) AddServiceToProvider(service string) ProviderGenerator {
	newProvider := deepCopyProvider(p.baseProvider)
	p.Providers[newProvider] = true
	p.Services[service] = true
	p.providerToService[newProvider] = service
	p.serviceToProvider[service] = newProvider

	return newProvider
}

// RemoveServices drops services and their providers.
func (p *ProvidersMapping) RemoveServices(services []string) {
	for _, service := range services {
		delete(p.Services, service)

		matchingProvider := p.serviceToProvider[service]
		delete(p.Providers, matchingProvider)
		delete(p.providerToService, matchingProvider)
		delete(p.serviceToProvider, service)
	}
}

// ProcessResources collects the resources each service's provider listed.
func (p *ProvidersMapping) ProcessResources() {
	for provider := range p.Providers {
		resources := provider.GetService().GetResources()
		log.Printf("Number of resources for service %s: %d", p.providerToService[provider], len(resources))
		for i := range resources {
			resource := resources[i]
			p.Resources[&resource] = true
			p.resourceToProvider[&resource] = provider
		}
	}
}

// GetResourcesByService returns the collected resources by service. Every
// service is a key, with no resources if it listed none.
func (p *ProvidersMapping) GetResourcesByService() map[string][]Resource {
	mapping := map[string][]Resource{}
	for service := range p.Services {
		mapping[service] = []Resource{}
	}

	for resource := range p.Resources {
		provider := p.resourceToProvider[resource]
		service := p.providerToService[provider]
		mapping[service] = append(mapping[service], *resource)
	}

	return mapping
}
