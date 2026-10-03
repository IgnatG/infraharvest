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

package terraformutils

import (
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

type ProviderGenerator interface {
	Init(args []string) error
	InitService(serviceName string, verbose bool) error
	GetName() string
	GetService() ServiceGenerator
	GetConfig() cty.Value
	GetBasicConfig() cty.Value
	GetSupportedService() map[string]ServiceGenerator
	GenerateFiles()
	GetProviderData(arg ...string) map[string]interface{}
	GenerateOutputPath() error
	GetResourceConnections() map[string]map[string][]string
}

type ProviderWithSource interface {
	GetSource() string
}

// ProviderWithImportIDs maps a listed resource to the ID Terraform imports it
// with (--engine=terraform), for listers that record a different ID. ok is
// false for resource types Terraform can't import.
type ProviderWithImportIDs interface {
	ImportID(r Resource) (id string, ok bool)
}

// ProviderWithConfigFixups repairs one resource block of the configuration
// Terraform generates (--engine=terraform) where the provider's generator
// is known to write invalid configuration. It reports whether it changed
// anything.
type ProviderWithConfigFixups interface {
	FixGeneratedConfig(resourceType string, body *hclwrite.Body) bool
}

type Provider struct {
	Service ServiceGenerator
	Config  cty.Value
}

func (p *Provider) Init(args []string) error {
	panic("implement me")
}

func (p *Provider) GetConfig() cty.Value {
	return p.Config
}

func (p *Provider) GetName() string {
	panic("implement me")
}

func (p *Provider) InitService(serviceName string) error {
	panic("implement me")
}

func (p *Provider) GenerateOutputPath() error {
	panic("implement me")
}

func (p *Provider) GenerateFiles() {
	panic("implement me")
}

func (p *Provider) GetService() ServiceGenerator {
	return p.Service
}

func (p *Provider) GetSupportedService() map[string]ServiceGenerator {
	panic("implement me")
}

func (p *Provider) GetBasicConfig() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{})
}
