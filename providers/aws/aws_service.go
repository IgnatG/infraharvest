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
	"context"
	"os"
	"regexp"
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

var awsVariable = regexp.MustCompile(`(\${[0-9A-Za-z:]+})`)

// configKey identifies an SDK config: one import can cover several regions,
// global services included.
type configKey struct{ region, profile string }

var (
	configsMu sync.Mutex
	configs   = map[configKey]aws.Config{}
	// testConfig, when set, is returned for every region and profile.
	testConfig *aws.Config
)

func (s *AWSService) generateConfig() (aws.Config, error) {
	if testConfig != nil {
		return *testConfig, nil
	}
	key := configKey{region: s.GetArgs()["region"].(string), profile: s.GetArgs()["profile"].(string)}
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
		baseConfig.ClientLogMode = aws.LogRequestWithBody & aws.LogResponseWithBody
	}

	creds, e := baseConfig.Credentials.Retrieve(context.TODO())

	if e != nil {
		return baseConfig, e
	}

	// terraform cannot ask for MFA token, so we need to pass STS session token, which might contain credentials with MFA requirement
	accessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey == "" {
		os.Setenv("AWS_ACCESS_KEY_ID", creds.AccessKeyID)
		os.Setenv("AWS_SECRET_ACCESS_KEY", creds.SecretAccessKey)

		if creds.SessionToken != "" {
			os.Setenv("AWS_SESSION_TOKEN", creds.SessionToken)
		}
	}
	configs[key] = baseConfig
	return baseConfig, nil
}

func (s *AWSService) buildBaseConfig() (aws.Config, error) {
	var loadOptions []func(*config.LoadOptions) error
	if s.GetArgs()["profile"].(string) != "" {
		loadOptions = append(loadOptions, config.WithSharedConfigProfile(s.GetArgs()["profile"].(string)))
	}
	if s.GetArgs()["region"].(string) != "" {
		os.Setenv("AWS_REGION", s.GetArgs()["region"].(string))
	}
	loadOptions = append(loadOptions, config.WithAssumeRoleCredentialOptions(func(options *stscreds.AssumeRoleOptions) {
		options.TokenProvider = stscreds.StdinTokenProvider
	}))
	return config.LoadDefaultConfig(context.TODO(), loadOptions...)
}

// for CF interpolation and IAM Policy variables
func (*AWSService) escapeAwsInterpolation(str string) string {
	return awsVariable.ReplaceAllString(str, "$$$1")
}

func (s *AWSService) getAccountNumber(config aws.Config) (*string, error) {
	stsSvc := sts.NewFromConfig(config)
	identity, err := stsSvc.GetCallerIdentity(context.TODO(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return identity.Account, nil
}
