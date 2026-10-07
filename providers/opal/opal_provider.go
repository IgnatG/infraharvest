// Copyright 2022 The Terraformer Authors.
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

package opal

import (
	"errors"
	"os"

	"github.com/IgnatG/infraharvest/terraformutils"
)

const opalDefaultURL = "https://api.opal.dev"

type OpalProvider struct { //nolint
	terraformutils.Provider
	token   string
	baseURL string
}

func (p OpalProvider) GetProviderData(_ ...string) map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			"opal": map[string]interface{}{
				"base_url": p.baseURL,
			},
		},
	}
}

func (p *OpalProvider) GetName() string {
	return "opal"
}

func (p *OpalProvider) GetSource() string {
	return "opalsecurity/opal"
}

func (p *OpalProvider) Init(_ []string) error {
	p.token = os.Getenv("OPAL_AUTH_TOKEN")
	if p.token == "" {
		return errors.New("the Opal API key must be set via `OPAL_AUTH_TOKEN` env var")
	}
	p.baseURL = os.Getenv("OPAL_BASE_URL")
	if p.baseURL == "" {
		p.baseURL = opalDefaultURL
	}

	return nil
}

func (p *OpalProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("opal: " + serviceName + " is not a supported resource type")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(map[string]interface{}{
		"token":    p.token,
		"base_url": p.baseURL,
	})
	return nil
}

func (p *OpalProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"owner":            &OwnerGenerator{},
		"resource":         &ResourceGenerator{},
		"group":            &GroupGenerator{},
		"message_channel":  &MessageChannelGenerator{},
		"on_call_schedule": &OnCallScheduleGenerator{},
	}
}
