// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// roleSessionName names the sessions of the roles the provider assumes, as
// CloudTrail shows them.
const roleSessionName = "infraharvest"

// defaultMaxAttempts is how often a call is tried, unless AWS_MAX_ATTEMPTS
// says otherwise: enough for the adaptive retry mode to ride out the
// throttling of a large account.
const defaultMaxAttempts = 10

// Service is what every service of the provider embeds: the harvest
// state, and the SDK configuration its arguments pick (region, profile
// and role_arn, see Provider.serviceArgs).
type Service struct {
	harvest.ListerBase
}

// configKey picks one SDK configuration: the services of one account and
// region share it.
type configKey struct {
	region, profile, roleARN string
}

var (
	// configMu guards configs and baseProviders, and is held while a
	// configuration is built, so that the first service to need one builds
	// it and the others wait for it: building may prompt for an MFA code.
	configMu sync.Mutex
	// configs are the configurations built so far.
	configs = map[configKey]aws.Config{}
	// baseProviders are the credentials of each configuration before any
	// role is assumed, which Terraform gets (see baseCredentials).
	baseProviders = map[configKey]aws.CredentialsProvider{}
	// testConfig, when set, is the configuration of every service, and its
	// credentials the base credentials. Tests set it to fake the API.
	testConfig *aws.Config
)

// cacheKey returns the key of the service's configuration, from its
// arguments.
func (s *Service) cacheKey() configKey {
	arg := func(name string) string {
		v, _ := s.Settings()[name].(string)
		return v
	}
	return configKey{region: arg("region"), profile: arg("profile"), roleARN: arg("role_arn")}
}

// sdkConfig returns the SDK configuration of the service's account
// and region, built the first time it is asked for and shared after that.
// The credentials are resolved, and the role assumed, while it is built,
// so that a configuration it returns works.
func (s *Service) sdkConfig() (aws.Config, error) {
	if testConfig != nil {
		return *testConfig, nil
	}
	key := s.cacheKey()
	configMu.Lock()
	defer configMu.Unlock()
	if cfg, ok := configs[key]; ok {
		return cfg, nil
	}
	cfg, base, err := s.buildConfig(key)
	if err != nil {
		return aws.Config{}, err
	}
	configs[key] = cfg
	baseProviders[key] = base
	return cfg, nil
}

// buildConfig loads the configuration of key, resolves its credentials and,
// with a role, assumes it. It returns the configuration and its credentials
// from before any role. It never sets an environment variable: the region
// and the profile are given to the SDK directly.
func (s *Service) buildConfig(key configKey) (aws.Config, aws.CredentialsProvider, error) {
	ctx := s.Context()
	cfg, err := config.LoadDefaultConfig(ctx, s.configOptions(key)...)
	if err != nil {
		return aws.Config{}, nil, err
	}

	// Each configuration, so each account and region, gets its own limit
	// on the requests to each API host.
	base := cfg.HTTPClient
	if base == nil {
		base = awshttp.NewBuildableClient()
	}
	cfg.HTTPClient = ratelimit.Like(ratelimit.Default()).Client(base)

	if cfg.Credentials == nil {
		return aws.Config{}, nil, errors.New("no AWS credentials found")
	}
	baseProvider := cfg.Credentials
	if _, ok := baseProvider.(*aws.CredentialsCache); !ok {
		baseProvider = aws.NewCredentialsCache(baseProvider)
	}
	cfg.Credentials = baseProvider
	if _, err := baseProvider.Retrieve(ctx); err != nil {
		return aws.Config{}, nil, err
	}

	if key.roleARN != "" {
		role := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), key.roleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = roleSessionName
		})
		cfg.Credentials = aws.NewCredentialsCache(role)
		if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
			return aws.Config{}, nil, fmt.Errorf("assume %s: %w", key.roleARN, err)
		}
	}
	return cfg, baseProvider, nil
}

// configOptions are the options the configuration of key loads with.
func (s *Service) configOptions(key configKey) []func(*config.LoadOptions) error {
	options := []func(*config.LoadOptions) error{
		// A profile's role that needs MFA asks for the code on the
		// terminal.
		config.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
			o.TokenProvider = stscreds.StdinTokenProvider
		}),
	}
	// The default profile is what the SDK uses anyway; naming it would
	// fail without a shared config file, as with credentials from the
	// environment only.
	if key.profile != "" && key.profile != "default" {
		options = append(options, config.WithSharedConfigProfile(key.profile))
	}
	// GlobalPseudoRegion is passed as it is: the SDK resolves the global
	// endpoints for it.
	if key.region != ProfileRegion {
		options = append(options, config.WithRegion(key.region))
	}
	if os.Getenv("AWS_RETRY_MODE") == "" {
		options = append(options, config.WithRetryMode(aws.RetryModeAdaptive))
	}
	if os.Getenv("AWS_MAX_ATTEMPTS") == "" {
		options = append(options, config.WithRetryMaxAttempts(defaultMaxAttempts))
	}
	if s.Verbose() {
		// Headers only: bodies can hold secrets.
		options = append(options, config.WithClientLogMode(aws.LogRequest|aws.LogResponse|aws.LogRetries))
	}
	return options
}

// callerAccount returns the ID of the account cfg's credentials are of.
func (s *Service) callerAccount(cfg aws.Config) (*string, error) {
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(s.Context(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return identity.Account, nil
}

// baseCredentials returns the credentials of the service's configuration
// from before any role is assumed, refreshed if they have expired.
func (s *Service) baseCredentials() (aws.Credentials, error) {
	if testConfig != nil {
		if testConfig.Credentials == nil {
			return aws.Credentials{}, errors.New("the test configuration has no credentials")
		}
		return testConfig.Credentials.Retrieve(s.Context())
	}
	if _, err := s.sdkConfig(); err != nil {
		return aws.Credentials{}, err
	}
	configMu.Lock()
	provider := baseProviders[s.cacheKey()]
	configMu.Unlock()
	if provider == nil {
		return aws.Credentials{}, errors.New("no AWS credentials found")
	}
	return provider.Retrieve(s.Context())
}
