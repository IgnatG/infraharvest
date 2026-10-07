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

package rabbitmq

import (
	"errors"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type RBTProvider struct { //nolint
	terraformutils.Provider
	endpoint string
	username string
	password string
}

func (p *RBTProvider) Init(args []string) error {
	p.endpoint = args[0]
	p.username = args[1]
	p.password = args[2]
	return nil
}

func (p *RBTProvider) GetName() string {
	return "rabbitmq"
}

func (p *RBTProvider) GetProviderData(_ ...string) map[string]interface{} {
	return map[string]interface{}{}
}

func (p *RBTProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New(p.GetName() + ": " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(map[string]interface{}{
		"endpoint": p.endpoint,
		"username": p.username,
		"password": p.password,
	})
	return nil
}

func (p *RBTProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"bindings":    &BindingGenerator{},
		"exchanges":   &ExchangeGenerator{},
		"permissions": &PermissionsGenerator{},
		"policies":    &PolicyGenerator{},
		"queues":      &QueueGenerator{},
		"users":       &UserGenerator{},
		"vhosts":      &VhostGenerator{},
		"shovels":     &ShovelGenerator{},
	}
}

// GetSource is the provider's registry source, for required_providers.
func (p *RBTProvider) GetSource() string {
	return "cyrilgdn/rabbitmq"
}
