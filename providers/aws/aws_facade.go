package aws

import (
	"context"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/smithy-go"
)

type AwsFacade struct { //nolint
	AWSService
	service terraformutils.ServiceGenerator
}

func (s *AwsFacade) SetProviderName(providerName string) {
	s.service.SetProviderName(providerName)
}

func (s *AwsFacade) SetVerbose(verbose bool) {
	s.service.SetVerbose(verbose)
}

func (s *AwsFacade) SetContext(ctx context.Context) {
	s.service.SetContext(ctx)
}

func (s *AwsFacade) ParseFilters(rawFilters []string) {
	s.service.ParseFilters(rawFilters)
}

func (s *AwsFacade) ParseFilter(rawFilter string) []terraformutils.ResourceFilter {
	return s.service.ParseFilter(rawFilter)
}

func (s *AwsFacade) SetName(name string) {
	s.service.SetName(name)
}
func (s *AwsFacade) GetName() string {
	return s.service.GetName()
}

func (s *AwsFacade) InitialCleanup() {
	s.service.InitialCleanup()
}

func (s *AwsFacade) GetArgs() map[string]interface{} {
	return s.service.GetArgs()
}
func (s *AwsFacade) SetArgs(args map[string]interface{}) {
	s.service.SetArgs(args)
}

func (s *AwsFacade) GetResources() []terraformutils.Resource {
	return s.service.GetResources()
}
func (s *AwsFacade) SetResources(resources []terraformutils.Resource) {
	s.service.SetResources(resources)
}

// InitResources lists the service. A service AWS doesn't offer in the region
// is skipped with a log message; any other error, including a timeout, is
// returned, so a failed listing isn't mistaken for an empty one.
func (s *AwsFacade) InitResources() error {
	err := s.service.InitResources()
	if err == nil || !unavailableInRegion(err) {
		return err
	}
	log.Printf("aws: %s isn't available in this region; skipping it (%v)", s.service.GetName(), err)
	return nil
}

// unavailableInRegion reports whether err says the service doesn't exist in
// the region: its endpoint has no DNS name, answers with a certificate for
// another host, or the API reports the operation unavailable.
func unavailableInRegion(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "UnavailableOperation" || strings.Contains(apiErr.ErrorMessage(), "Unavailable Operation")
	}
	return false
}
