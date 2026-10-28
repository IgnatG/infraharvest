// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"strings"

	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// guardedLister wraps the lister of every service of the provider. It
// passes everything on to the lister, and adds to its listing what all
// AWS services share: Resource Explorer may rule the service out (see
// explorerSkip), and a service AWS doesn't offer in the region lists
// nothing there rather than failing.
type guardedLister struct {
	// Service holds the context explorerSkip reads with; the rest of
	// the service's state is the lister's.
	Service
	lister harvest.Lister
	// resourceExplorer lets Resource Explorer rule the service out (see
	// Provider.UseResourceExplorer).
	resourceExplorer bool
}

// List lists the service, unless Resource Explorer shows the
// region has none of its resources, or AWS doesn't offer it there.
func (s *guardedLister) List() error {
	if reason := s.explorerSkip(); reason != "" {
		log.Printf("aws: %s: %s; not listing it", s.lister.Name(), reason)
		return nil
	}
	err := s.lister.List()
	if err != nil && unavailableInRegion(err) {
		log.Printf("aws: %s: not offered in %s: %v; not listing it", s.lister.Name(), s.where(), err)
		return nil
	}
	return err
}

// where names the region the service lists, for a log.
func (s *guardedLister) where() string {
	region := s.Settings().region
	if region == ProfileRegion {
		return "the default region"
	}
	return region
}

// unavailableInRegion says whether err means AWS doesn't offer the service
// in the region: it has no endpoint there, the endpoint's name is served
// by another host's certificate, or the API says the operation is
// unavailable. A timeout or a DNS failure that may pass is not that.
func unavailableInRegion(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	var hostErrPtr *x509.HostnameError
	if errors.As(err, &hostErrPtr) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "UnavailableOperation" || strings.Contains(apiErr.ErrorMessage(), "Unavailable Operation")
	}
	return false
}

// SetContext sets the context of the guard, which explorerSkip uses, and
// of the lister.
func (s *guardedLister) SetContext(ctx context.Context) {
	s.Service.SetContext(ctx)
	s.lister.SetContext(ctx)
}

// SetSettings sets the settings of the guard, which where and explorerSkip
// read, and of the lister, when it takes them.
func (s *guardedLister) SetSettings(settings serviceSettings) {
	s.Service.SetSettings(settings)
	if l, ok := s.lister.(harvest.SettingsTaker[serviceSettings]); ok {
		l.SetSettings(settings)
	}
}

// The other methods of harvest.Lister are the lister's.

func (s *guardedLister) Listed() []harvest.Resource {
	return s.lister.Listed()
}

func (s *guardedLister) SetListed(resources []harvest.Resource) {
	s.lister.SetListed(resources)
}

func (s *guardedLister) SetFilters(filters []harvest.Filter) {
	s.lister.SetFilters(filters)
}

func (s *guardedLister) Configure(config harvest.ListerConfig) {
	s.lister.Configure(config)
}

func (s *guardedLister) Name() string {
	return s.lister.Name()
}

func (s *guardedLister) Provider() string {
	return s.lister.Provider()
}

func (s *guardedLister) Prune() {
	s.lister.Prune()
}
