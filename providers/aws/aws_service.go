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

package aws

import (
	"fmt"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type AWSService struct { //nolint
	terraformutils.Service
}

// listMaxAttempts is how many times a throttled or failed call is tried.
const listMaxAttempts = 10

// configKey identifies an SDK config: one import can cover several regions,
// global services included, and several accounts, through roles.
type configKey struct{ region, profile, roleARN string }

var (
	configsMu sync.Mutex
	configs   = map[configKey]aws.Config{}
	// baseCredentials are the credentials each configuration started from,
	// before assuming a role: Terraform's (see AWSProvider.TerraformEnv).
	baseCredentials = map[configKey]aws.CredentialsProvider{}
	// testConfig, when set, is returned for every region and profile.
	testConfig *aws.Config
)

func (s *AWSService) generateConfig() (aws.Config, error) {
	if testConfig != nil {
		return *testConfig, nil
	}
	roleARN, _ := s.GetArgs()["role_arn"].(string)
	key := configKey{region: s.GetArgs()["region"].(string), profile: s.GetArgs()["profile"].(string), roleARN: roleARN}
	configsMu.Lock()
	defer configsMu.Unlock()
	if cfg, ok := configs[key]; ok {
		return cfg, nil
	}

	baseConfig, e := s.buildBaseConfig()

	if e != nil {
		return baseConfig, e
	}
	if s.Verbose {
		// Headers only: bodies would log STS credentials.
		baseConfig.ClientLogMode = aws.LogRequest | aws.LogResponse | aws.LogRetries
	}

	if _, e := baseConfig.Credentials.Retrieve(s.Context()); e != nil {
		return baseConfig, e
	}
	baseCredentials[key] = baseConfig.Credentials
	// The base credentials go to Terraform, whose provider block assumes the
	// role itself (see GetProviderData).
	if roleARN != "" {
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(baseConfig), roleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = "infraharvest"
		})
		baseConfig.Credentials = aws.NewCredentialsCache(provider)
		if _, e := baseConfig.Credentials.Retrieve(s.Context()); e != nil {
			return baseConfig, fmt.Errorf("assume %s: %w", roleARN, e)
		}
	}
	configs[key] = baseConfig
	return baseConfig, nil
}

func (s *AWSService) buildBaseConfig() (aws.Config, error) {
	var loadOptions []func(*config.LoadOptions) error
	// --profile defaults to "default", which must not require a shared config
	// file: in CI, credentials often come only from the environment (OIDC).
	// The SDK's default chain uses that profile anyway, or AWS_PROFILE.
	if profile := s.GetArgs()["profile"].(string); profile != "" && profile != "default" {
		loadOptions = append(loadOptions, config.WithSharedConfigProfile(profile))
	}
	// Per configuration, not through AWS_REGION: several regions can list
	// in one process.
	if region := s.GetArgs()["region"].(string); region != "" {
		loadOptions = append(loadOptions, config.WithRegion(region))
	}
	loadOptions = append(loadOptions, config.WithAssumeRoleCredentialOptions(func(options *stscreds.AssumeRoleOptions) {
		options.TokenProvider = stscreds.StdinTokenProvider
	}))
	// Listing makes many calls in a row: throttled calls back off and retry,
	// and the adaptive mode also slows the client down while an API
	// throttles it. AWS_RETRY_MODE and AWS_MAX_ATTEMPTS still win.
	if os.Getenv("AWS_RETRY_MODE") == "" {
		loadOptions = append(loadOptions, config.WithRetryMode(aws.RetryModeAdaptive))
	}
	if os.Getenv("AWS_MAX_ATTEMPTS") == "" {
		loadOptions = append(loadOptions, config.WithRetryMaxAttempts(listMaxAttempts))
	}
	return config.LoadDefaultConfig(s.Context(), loadOptions...)
}

func (s *AWSService) getAccountNumber(config aws.Config) (*string, error) {
	stsSvc := sts.NewFromConfig(config)
	identity, err := stsSvc.GetCallerIdentity(s.Context(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return identity.Account, nil
}

// baseCredentials returns the credentials the service's configuration
// started from, before assuming a role, refreshed if they expired.
func (s *AWSService) baseCredentials() (aws.Credentials, error) {
	cfg, err := s.generateConfig()
	if err != nil {
		return aws.Credentials{}, err
	}
	provider := cfg.Credentials
	if testConfig == nil {
		roleARN, _ := s.GetArgs()["role_arn"].(string)
		key := configKey{region: s.GetArgs()["region"].(string), profile: s.GetArgs()["profile"].(string), roleARN: roleARN}
		configsMu.Lock()
		provider = baseCredentials[key]
		configsMu.Unlock()
	}
	return provider.Retrieve(s.Context())
}
