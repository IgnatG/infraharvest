package terraformutils

import (
	"fmt"
	"log"
	"math/rand"
	"reflect"
)

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

func (p *ProvidersMapping) GetBaseProvider() ProviderGenerator {
	return p.baseProvider
}

func (p *ProvidersMapping) AddServiceToProvider(service string) ProviderGenerator {
	newProvider := deepCopyProvider(p.baseProvider)
	p.Providers[newProvider] = true
	p.Services[service] = true
	p.providerToService[newProvider] = service
	p.serviceToProvider[service] = newProvider

	return newProvider
}

func (p *ProvidersMapping) GetServices() []string {
	services := make([]string, 0, len(p.Services))
	for service := range p.Services {
		services = append(services, service)
	}

	return services
}

func (p *ProvidersMapping) RemoveServices(services []string) {
	for _, service := range services {
		delete(p.Services, service)

		matchingProvider := p.serviceToProvider[service]
		delete(p.Providers, matchingProvider)
		delete(p.providerToService, matchingProvider)
		delete(p.serviceToProvider, service)
	}
}

func (p *ProvidersMapping) ShuffleResources() []*Resource {
	resources := []*Resource{}
	for resource := range p.Resources {
		resources = append(resources, resource)
	}
	rand.Shuffle(len(resources), func(i, j int) { resources[i], resources[j] = resources[j], resources[i] })

	return resources
}

func (p *ProvidersMapping) ProcessResources(isCleanup bool) {
	initialResources := p.resourceToProvider
	if isCleanup && len(initialResources) > 0 {
		p.Resources = map[*Resource]bool{}
		p.resourceToProvider = map[*Resource]ProviderGenerator{}
		for provider := range p.Providers {
			resources := provider.GetService().GetResources()
			log.Printf("Filtered number of resources for service %s: %d", p.providerToService[provider], len(provider.GetService().GetResources()))
			for i := range resources {
				resource := resources[i]
				p.Resources[&resource] = true
				p.resourceToProvider[&resource] = provider
			}
		}
	} else if !isCleanup {
		for provider := range p.Providers {
			resources := provider.GetService().GetResources()
			log.Printf("Number of resources for service %s: %d", p.providerToService[provider], len(provider.GetService().GetResources()))
			for i := range resources {
				resource := resources[i]
				p.Resources[&resource] = true
				p.resourceToProvider[&resource] = provider
			}
		}
	}
}

func (p *ProvidersMapping) MatchProvider(resource *Resource) ProviderGenerator {
	return p.resourceToProvider[resource]
}

func (p *ProvidersMapping) SetResources(resourceToKeep []*Resource) {
	p.Resources = map[*Resource]bool{}
	resourcesGroupsByProviders := map[ProviderGenerator][]Resource{}
	for i := range resourceToKeep {
		resource := resourceToKeep[i]
		provider := p.resourceToProvider[resource]
		if resourcesGroupsByProviders[provider] == nil {
			resourcesGroupsByProviders[provider] = []Resource{}
		}
		resourcesGroupsByProviders[provider] = append(resourcesGroupsByProviders[provider], *resource)
		p.Resources[resource] = true
	}

	for provider := range p.Providers {
		provider.GetService().SetResources(resourcesGroupsByProviders[provider])
	}
}

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

// ConvertTFStates converts each resource's state to configuration. A resource
// that fails to convert is dropped and reported in the returned errors.
func (p *ProvidersMapping) ConvertTFStates(schemaProvider SchemaProvider) []error {
	var failures []error
	for resource := range p.Resources {
		if err := resource.ConvertTFstate(schemaProvider); err != nil {
			failures = append(failures, fmt.Errorf("convert %s: %w", resource.InstanceInfo.Id, err))
			delete(p.Resources, resource)
		}
	}
	sortErrors(failures)

	resourcesGroupsByProviders := map[ProviderGenerator][]Resource{}
	for resource := range p.Resources {
		provider := p.resourceToProvider[resource]
		if resourcesGroupsByProviders[provider] == nil {
			resourcesGroupsByProviders[provider] = []Resource{}
		}
		resourcesGroupsByProviders[provider] = append(resourcesGroupsByProviders[provider], *resource)
	}

	for provider := range p.Providers {
		provider.GetService().SetResources(resourcesGroupsByProviders[provider])
	}
	return failures
}

// CleanupProviders runs each service's post-refresh cleanup and post-convert
// hook. It returns one error per service whose hook failed; that service's
// output may be missing the hook's adjustments.
func (p *ProvidersMapping) CleanupProviders() []error {
	var failures []error
	for provider := range p.Providers {
		provider.GetService().PostRefreshCleanup()
		if err := provider.GetService().PostConvertHook(); err != nil {
			failures = append(failures, fmt.Errorf("post-convert hook for service %s: %w", p.providerToService[provider], err))
		}
	}
	p.ProcessResources(true)
	sortErrors(failures)
	return failures
}
