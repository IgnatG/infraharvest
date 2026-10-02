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

package providerwrapper //nolint

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/terraformutils/terraformerstring"

	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	goversion "github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform/configs/configschema"
	tfplugin "github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/hashicorp/terraform/terraform"
	"github.com/hashicorp/terraform/version"
)

// DefaultDataDir is the default directory for storing local data.
const DefaultDataDir = ".terraform"

// DefaultPluginVendorDir is the location in the config directory to look for
// user-added plugin binaries. Terraform only reads from this path if it
// exists, it is never created by terraform.
const DefaultPluginVendorDirV12 = "terraform.d/plugins/" + pluginMachineName

// pluginMachineName is the directory name used in new plugin paths.
const pluginMachineName = runtime.GOOS + "_" + runtime.GOARCH

type ProviderWrapper struct {
	Provider     providers.Interface
	client       *plugin.Client
	rpcClient    plugin.ClientProtocol
	providerName string
	config       cty.Value
	schema       *providers.GetSchemaResponse
	retryCount   int
	retrySleepMs int
}

func NewProviderWrapper(providerName string, providerConfig cty.Value, verbose bool, options ...map[string]int) (*ProviderWrapper, error) {
	p := &ProviderWrapper{retryCount: 5, retrySleepMs: 300}
	p.providerName = providerName
	p.config = providerConfig

	if len(options) > 0 {
		retryCount, hasOption := options[0]["retryCount"]
		if hasOption {
			p.retryCount = retryCount
		}
		retrySleepMs, hasOption := options[0]["retrySleepMs"]
		if hasOption {
			p.retrySleepMs = retrySleepMs
		}
	}

	if err := p.initProvider(verbose); err != nil {
		if p.client != nil {
			p.client.Kill()
		}
		return nil, err
	}
	return p, nil
}

func (p *ProviderWrapper) Kill() {
	p.client.Kill()
}

func (p *ProviderWrapper) GetSchema() *providers.GetSchemaResponse {
	if p.schema == nil {
		r := p.Provider.GetSchema()
		p.schema = &r
	}
	return p.schema
}

func (p *ProviderWrapper) GetReadOnlyAttributes(resourceTypes []string) (map[string][]string, error) {
	r := p.GetSchema()

	if r.Diagnostics.HasErrors() {
		return nil, r.Diagnostics.Err()
	}
	readOnlyAttributes := map[string][]string{}
	for resourceName, obj := range r.ResourceTypes {
		if terraformerstring.ContainsString(resourceTypes, resourceName) {
			readOnlyAttributes[resourceName] = append(readOnlyAttributes[resourceName], "^id$")
			for k, v := range obj.Block.Attributes {
				if !v.Optional && !v.Required {
					if v.Type.IsListType() || v.Type.IsSetType() {
						readOnlyAttributes[resourceName] = append(readOnlyAttributes[resourceName], "^"+k+"\\.(.*)")
					} else {
						readOnlyAttributes[resourceName] = append(readOnlyAttributes[resourceName], "^"+k+"$")
					}
				}
			}
			readOnlyAttributes[resourceName] = p.readObjBlocks(obj.Block.BlockTypes, readOnlyAttributes[resourceName], "-1")
		}
	}
	return readOnlyAttributes, nil
}

func (p *ProviderWrapper) readObjBlocks(block map[string]*configschema.NestedBlock, readOnlyAttributes []string, parent string) []string {
	for k, v := range block {
		if len(v.BlockTypes) > 0 {
			if parent == "-1" {
				readOnlyAttributes = p.readObjBlocks(v.BlockTypes, readOnlyAttributes, k)
			} else {
				readOnlyAttributes = p.readObjBlocks(v.BlockTypes, readOnlyAttributes, parent+"\\.[0-9]+\\."+k)
			}
		}
		fieldCount := 0
		for key, l := range v.Attributes {
			if !l.Optional && !l.Required {
				fieldCount++
				switch v.Nesting {
				case configschema.NestingList:
					if parent == "-1" {
						readOnlyAttributes = append(readOnlyAttributes, "^"+k+"\\.[0-9]+\\."+key+"($|\\.[0-9]+|\\.#)")
					} else {
						readOnlyAttributes = append(readOnlyAttributes, "^"+parent+"\\.(.*)\\."+key+"$")
					}
				case configschema.NestingSet:
					if parent == "-1" {
						readOnlyAttributes = append(readOnlyAttributes, "^"+k+"\\.[0-9]+\\."+key+"$")
					} else {
						readOnlyAttributes = append(readOnlyAttributes, "^"+parent+"\\.(.*)\\."+key+"($|\\.(.*))")
					}
				case configschema.NestingMap:
					readOnlyAttributes = append(readOnlyAttributes, parent+"\\."+key)
				default:
					readOnlyAttributes = append(readOnlyAttributes, parent+"\\."+key+"$")
				}
			}
		}
		if fieldCount == len(v.Block.Attributes) && fieldCount > 0 && len(v.BlockTypes) == 0 {
			readOnlyAttributes = append(readOnlyAttributes, "^"+k)
		}
	}
	return readOnlyAttributes
}

func (p *ProviderWrapper) Refresh(info *terraform.InstanceInfo, state *terraform.InstanceState) (*terraform.InstanceState, error) {
	schema := p.GetSchema()
	impliedType := schema.ResourceTypes[info.Type].Block.ImpliedType()
	priorState, err := state.AttrsAsObjectValue(impliedType)
	if err != nil {
		return nil, err
	}
	successReadResource := false
	resp := providers.ReadResourceResponse{}
	for i := 0; i < p.retryCount; i++ {
		resp = p.Provider.ReadResource(providers.ReadResourceRequest{
			TypeName:   info.Type,
			PriorState: priorState,
			Private:    []byte{},
		})
		if resp.Diagnostics.HasErrors() {
			log.Println(resp.Diagnostics.Err())
			log.Printf("WARN: Fail read resource from provider, wait %dms before retry\n", p.retrySleepMs)
			time.Sleep(time.Duration(p.retrySleepMs) * time.Millisecond)
			continue
		} else {
			successReadResource = true
			break
		}
	}

	if !successReadResource {
		log.Println("Fail read resource from provider, trying import command")
		// retry with regular import command - without resource attributes
		importResponse := p.Provider.ImportResourceState(providers.ImportResourceStateRequest{
			TypeName: info.Type,
			ID:       state.ID,
		})
		if importResponse.Diagnostics.HasErrors() {
			return nil, fmt.Errorf("read failed: %w; import fallback failed: %w", resp.Diagnostics.Err(), importResponse.Diagnostics.Err())
		}
		if len(importResponse.ImportedResources) == 0 {
			return nil, errors.New("not able to import resource for a given ID")
		}
		return terraform.NewInstanceStateShimmedFromValue(importResponse.ImportedResources[0].State, int(schema.ResourceTypes[info.Type].Version)), nil
	}

	if resp.NewState.IsNull() {
		msg := fmt.Sprintf("ERROR: Read resource response is null for resource %s", info.Id)
		return nil, errors.New(msg)
	}

	return terraform.NewInstanceStateShimmedFromValue(resp.NewState, int(schema.ResourceTypes[info.Type].Version)), nil
}

func (p *ProviderWrapper) initProvider(verbose bool) error {
	providerFilePath, err := getProviderFileName(p.providerName)
	if err != nil {
		return err
	}
	options := hclog.LoggerOptions{
		Name:   "plugin",
		Level:  hclog.Error,
		Output: os.Stdout,
	}
	if verbose {
		options.Level = hclog.Trace
	}
	logger := hclog.New(&options)
	p.client = plugin.NewClient(
		&plugin.ClientConfig{
			Cmd:              exec.Command(providerFilePath),
			HandshakeConfig:  tfplugin.Handshake,
			VersionedPlugins: tfplugin.VersionedPlugins,
			Managed:          true,
			Logger:           logger,
			AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
			AutoMTLS:         true,
		})
	p.rpcClient, err = p.client.Client()
	if err != nil {
		return explainHandshakeError(p.providerName, providerFilePath, err)
	}
	raw, err := p.rpcClient.Dispense(tfplugin.ProviderPluginName)
	if err != nil {
		return err
	}

	p.Provider = raw.(*tfplugin.GRPCProvider)

	config, err := p.GetSchema().Provider.Block.CoerceValue(p.config)
	if err != nil {
		return err
	}
	resp := p.Provider.Configure(providers.ConfigureRequest{
		TerraformVersion: version.Version,
		Config:           config,
	})
	if resp.Diagnostics.HasErrors() {
		return fmt.Errorf("configure provider %s: %w", p.providerName, resp.Diagnostics.Err())
	}

	return nil
}

// explainHandshakeError makes go-plugin's protocol mismatch error actionable.
// Terraformer embeds Terraform 0.12, which only speaks plugin protocol 5, so
// provider releases built for protocol 6 cannot be loaded. go-plugin v1.4.4
// reports this only as an untyped error, hence the match on its message.
func explainHandshakeError(providerName, providerFilePath string, err error) error {
	if !strings.Contains(err.Error(), "Incompatible API version with plugin") {
		return err
	}
	return fmt.Errorf("provider %s (%s) uses a plugin protocol infraharvest cannot load (it supports only protocol 5); "+
		"install a provider release that still serves protocol 5 (see the provider's docs page): %w",
		providerName, providerFilePath, err)
}

func getProviderFileName(providerName string) (string, error) {
	defaultDataDir := os.Getenv("TF_DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = DefaultDataDir
	}
	providerFilePath, err := getProviderFileNameV13andV14(defaultDataDir, providerName)
	if err != nil || providerFilePath == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			providerFilePath, err = getProviderFileNameV13andV14(filepath.Join(home, ".terraform.d"), providerName)
		}
	}
	if err != nil || providerFilePath == "" {
		return getProviderFileNameV12(providerName)
	}
	return providerFilePath, nil
}

// getProviderFileNameV13andV14 returns the binary of the highest installed
// version of the provider in the registry layout under prefix, or "" if no
// version has a binary for this platform.
func getProviderFileNameV13andV14(prefix, providerName string) (string, error) {
	// Read terraform v14 file path
	registryDir := filepath.Join(prefix, "providers", "registry.terraform.io")
	namespaceDirs, err := os.ReadDir(registryDir)
	if err != nil {
		// Read terraform v13 file path
		registryDir = filepath.Join(prefix, "plugins", "registry.terraform.io")
		namespaceDirs, err = os.ReadDir(registryDir)
		if err != nil {
			return "", err
		}
	}
	providerFilePath := ""
	var highest *goversion.Version
	for _, namespaceDir := range namespaceDirs {
		pluginPath := filepath.Join(registryDir, namespaceDir.Name(), providerName)
		versionDirs, err := os.ReadDir(pluginPath)
		if err != nil {
			continue
		}
		for _, versionDir := range versionDirs {
			if !versionDir.IsDir() {
				continue
			}
			v, err := goversion.NewVersion(versionDir.Name())
			if err != nil || (highest != nil && !v.GreaterThan(highest)) {
				continue
			}
			if binary := findProviderBinary(filepath.Join(pluginPath, versionDir.Name(), pluginMachineName), providerName); binary != "" {
				highest, providerFilePath = v, binary
			}
		}
	}
	return providerFilePath, nil
}

func getProviderFileNameV12(providerName string) (string, error) {
	defaultDataDir := os.Getenv("TF_DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = DefaultDataDir
	}
	pluginPath := filepath.Join(defaultDataDir, "plugins", pluginMachineName)
	if _, err := os.Stat(pluginPath); err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", errors.Join(err, homeErr)
		}
		pluginPath = filepath.Join(home, "."+DefaultPluginVendorDirV12)
		if _, err := os.Stat(pluginPath); err != nil {
			return "", err
		}
	}
	return findProviderBinary(pluginPath, providerName), nil
}

// findProviderBinary returns the highest-versioned binary of the provider in
// dir, or "" if there is none. Binaries are named
// terraform-provider-NAME[_vX.Y.Z[_xN]][.exe]; unversioned ones rank lowest.
func findProviderBinary(dir, providerName string) string {
	files, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	binary := ""
	var highest *goversion.Version
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		v, ok := providerBinaryVersion(file.Name(), providerName)
		if !ok {
			continue
		}
		if binary == "" || (v != nil && (highest == nil || v.GreaterThan(highest))) {
			binary, highest = filepath.Join(dir, file.Name()), v
		}
	}
	return binary
}

// providerBinaryVersion reports whether fileName is a binary of the provider
// and returns its version, or nil if the name carries none.
func providerBinaryVersion(fileName, providerName string) (*goversion.Version, bool) {
	rest, found := strings.CutPrefix(strings.TrimSuffix(fileName, ".exe"), "terraform-provider-"+providerName)
	if !found {
		return nil, false
	}
	if rest == "" {
		return nil, true
	}
	if !strings.HasPrefix(rest, "_") {
		return nil, false // a different provider, e.g. awscc when looking for aws
	}
	v, err := goversion.NewVersion(strings.Split(rest[1:], "_")[0])
	if err != nil {
		return nil, true
	}
	return v, true
}

func GetProviderVersion(providerName string) string {
	providerFilePath, err := getProviderFileName(providerName)
	if err != nil {
		log.Println("Can't find provider file path. Ensure that you are following https://www.terraform.io/docs/configuration/providers.html#third-party-plugins.")
		return ""
	}
	t := strings.Split(providerFilePath, string(os.PathSeparator))
	providerFileName := t[len(t)-1]
	providerFileNameParts := strings.Split(providerFileName, "_")
	if len(providerFileNameParts) < 2 {
		log.Println("Can't find provider version. Ensure that you are following https://www.terraform.io/docs/configuration/providers.html#plugin-names-and-versions.")
		return ""
	}
	providerVersion := providerFileNameParts[1]
	return "~> " + strings.TrimPrefix(providerVersion, "v")
}
