package gcp

import (
	"context"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type GCPFacade struct { //nolint
	GCPService
	service terraformutils.ServiceGenerator
}

func (s *GCPFacade) SetProviderName(providerName string) {
	s.service.SetProviderName(providerName)
}

func (s *GCPFacade) GetProviderName() string {
	return s.service.GetProviderName()
}

func (s *GCPFacade) SetVerbose(verbose bool) {
	s.service.SetVerbose(verbose)
}

func (s *GCPFacade) SetContext(ctx context.Context) {
	s.service.SetContext(ctx)
}

func (s *GCPFacade) ParseFilters(rawFilters []string) {
	s.service.ParseFilters(rawFilters)
}

func (s *GCPFacade) ParseFilter(rawFilter string) []terraformutils.ResourceFilter {
	return s.service.ParseFilter(rawFilter)
}

func (s *GCPFacade) SetName(name string) {
	s.service.SetName(name)
}
func (s *GCPFacade) GetName() string {
	return s.service.GetName()
}

func (s *GCPFacade) InitialCleanup() {
	s.service.InitialCleanup()
}

func (s *GCPFacade) GetArgs() map[string]interface{} {
	return s.service.GetArgs()
}
func (s *GCPFacade) SetArgs(args map[string]interface{}) {
	s.service.SetArgs(args)
}

func (s *GCPFacade) GetResources() []terraformutils.Resource {
	return s.service.GetResources()
}
func (s *GCPFacade) SetResources(resources []terraformutils.Resource) {
	s.service.SetResources(resources)
}

func (s *GCPFacade) InitResources() error {
	err := s.service.InitResources()
	if err == nil {
		return nil
	}
	return err
}
