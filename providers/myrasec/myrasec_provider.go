package myrasec

import (
	"errors"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// MyrasecProvider
type MyrasecProvider struct {
	terraformutils.Provider
}

// Init
func (p *MyrasecProvider) Init(args []string) error {
	return nil
}

// GetName
func (p *MyrasecProvider) GetName() string {
	return "myrasec"
}

// GetProviderData
func (p *MyrasecProvider) GetProviderData(arg ...string) map[string]interface{} {
	return map[string]interface{}{}
}

// GetSupportedService
func (p *MyrasecProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"domain":        &DomainGenerator{},
		"dns_record":    &DNSGenerator{},
		"cache_setting": &CacheSettingGenerator{},
		"redirect":      &RedirectGenerator{},
		"ratelimit":     &RatelimitGenerator{},
		"ip_filter":     &IPFilterGenerator{},
		"settings":      &SettingsGenerator{},
		"waf_rule":      &WafRuleGenerator{},
		"maintenance":   &MaintenanceGenerator{},
		"error_page":    &ErrorPageGenerator{},
	}
}

// InitService
func (p *MyrasecProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("myrasec: " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())

	return nil
}

// GetSource is the provider's registry source, for required_providers.
func (p *MyrasecProvider) GetSource() string {
	return "Myra-Security-GmbH/myrasec"
}
