package opsgenie

import (
	"errors"
	"os"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type OpsgenieProvider struct { //nolint
	terraformutils.Provider

	APIKey string
}

func (p *OpsgenieProvider) Init(args []string) error {
	if apiKey := os.Getenv("OPSGENIE_API_KEY"); apiKey != "" {
		p.APIKey = os.Getenv("OPSGENIE_API_KEY")
	}
	if args[0] != "" {
		p.APIKey = args[0]
	}
	if p.APIKey == "" {
		return errors.New("required API Key missing")
	}

	return nil
}

func (p *OpsgenieProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New(p.GetName() + ": " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(map[string]interface{}{
		"api-key": p.APIKey,
	})
	return nil
}

func (p *OpsgenieProvider) GetProviderData(_ ...string) map[string]interface{} {
	return map[string]interface{}{}
}

func (p *OpsgenieProvider) GetName() string {
	return "opsgenie"
}

func (p *OpsgenieProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"user":    &UserGenerator{},
		"team":    &TeamGenerator{},
		"service": &ServiceGenerator{},
	}
}

// GetSource is the provider's registry source, for required_providers.
func (p *OpsgenieProvider) GetSource() string {
	return "opsgenie/opsgenie"
}
