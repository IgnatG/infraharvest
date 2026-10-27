// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/logging"
	"golang.org/x/sync/singleflight"

	"github.com/IgnatG/infraharvest/internal/harvest"
	ihlogging "github.com/IgnatG/infraharvest/internal/logging"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// The keys of the settings each service gets (see Provider.serviceArgs),
// written only by serviceSettings.args and read only by settingsOf, so
// that the two can't drift apart.
const (
	regionKey               = "region"
	profileKey              = "profile"
	roleARNKey              = "role_arn"
	skipRegionValidationKey = "skip_region_validation"
)

// serviceSettings are the settings each service gets, typed: the service
// gets them as a map (see harvest.ListerConfig), which args makes and
// settingsOf reads back. They pick the service's SDK configuration (see
// Service.sdkConfig).
type serviceSettings struct {
	// region is the region to list: a region's name, GlobalPseudoRegion or
	// ProfileRegion.
	region string
	// profile is the shared-config profile, "" or "default" for none.
	profile string
	// roleARN is the role to assume before listing, "" for none.
	roleARN string
}

// args returns the settings as a service's settings map. It also sets
// skip_region_validation, as the services have always been given it.
func (s serviceSettings) args() map[string]interface{} {
	return map[string]interface{}{
		regionKey:               s.region,
		profileKey:              s.profile,
		roleARNKey:              s.roleARN,
		skipRegionValidationKey: true,
	}
}

// settingsOf returns the settings in a service's settings map, as args
// makes it. A setting that is missing, or of another type, comes back as
// "".
func settingsOf(settings map[string]interface{}) serviceSettings {
	var s serviceSettings
	s.region, _ = settings[regionKey].(string)
	s.profile, _ = settings[profileKey].(string)
	s.roleARN, _ = settings[roleARNKey].(string)
	return s
}

// regionSetting returns the region in a service's settings, "" when there
// is none.
func regionSetting(settings map[string]interface{}) string {
	return settingsOf(settings).region
}

// roleSessionName names the sessions of the roles the provider assumes, as
// CloudTrail shows them.
const roleSessionName = "infraharvest"

// defaultMaxAttempts is how often a call is tried, unless AWS_MAX_ATTEMPTS
// says otherwise: enough for the adaptive retry mode to ride out the
// throttling of a large account.
const defaultMaxAttempts = 10

// credentialsExpiryWindow is how long before temporary credentials expire
// they are refreshed: what TerraformEnv hands a plan has at least that long
// left. It is short, as an assumed role's session may last only 15
// minutes, and refreshing one that needs MFA asks for a code again.
const credentialsExpiryWindow = 5 * time.Minute

// Service is what every service of the provider embeds: the harvest
// state, and the SDK configuration its arguments pick (region, profile
// and role_arn, see Provider.serviceArgs).
type Service struct {
	harvest.ListerBase
	// unwanted reports the types the import leaves out whatever is listed
	// (see Provider.SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
}

func (s *Service) setUnwanted(unwanted func(typ string) bool) {
	s.unwanted = unwanted
}

// wants reports whether the import may keep resources of typ: a lister
// skips the calls that list only resources of a type it doesn't want.
func (s *Service) wants(typ string) bool {
	return s.unwanted == nil || !s.unwanted(typ)
}

// configKey picks one SDK configuration: the services of one account and
// region share it.
type configKey struct {
	region, profile, roleARN string
}

// flightKey is the key as configFlight takes it.
func (k configKey) flightKey() string {
	return k.region + "\x00" + k.profile + "\x00" + k.roleARN
}

// credentialsKey picks the credentials of a configuration: every region of
// one profile and role shares them.
type credentialsKey struct {
	profile, roleARN string
}

// flightKey is the key as credentialsFlight takes it.
func (k credentialsKey) flightKey() string {
	return k.profile + "\x00" + k.roleARN
}

// sharedCredentials are the credentials of one profile and role, resolved
// once: base from before any role is assumed, and the credentials the
// services use, the assumed role's when there is one.
type sharedCredentials struct {
	base, credentials aws.CredentialsProvider
}

// buildFailure is a configuration that failed to build, and when.
type buildFailure struct {
	err error
	at  time.Time
}

// failureMemory is how long a configuration that failed to build fails
// again at once, instead of resolving the credentials again: long enough
// for every service of an import to fail fast, short enough for a server
// that outlives the import to try again once the credentials are fixed.
const failureMemory = time.Minute

var (
	// configMu guards configs, baseProviders, resolvedCredentials and
	// buildFailures. It isn't held while a configuration is built, so that
	// the configurations of different accounts and regions build at once.
	configMu sync.Mutex
	// configFlight builds each configuration once at a time: the first
	// service to need one builds it, and the others of its key wait for
	// it and share the answer.
	configFlight singleflight.Group
	// credentialsFlight resolves the credentials of each profile and role
	// once at a time, as resolving them may prompt for an MFA code: the
	// first region to need them resolves them, and the others wait for
	// them and share the answer.
	credentialsFlight singleflight.Group
	// configs are the configurations built so far.
	configs = map[configKey]aws.Config{}
	// baseProviders are the credentials of each configuration before any
	// role is assumed, which Terraform gets (see baseCredentials).
	baseProviders = map[configKey]aws.CredentialsProvider{}
	// resolvedCredentials are the credentials resolved so far, by profile
	// and role, which the configuration of every region of them shares: a
	// profile's role that needs MFA asks for the code once, not once per
	// region, and a role is assumed once.
	resolvedCredentials = map[credentialsKey]sharedCredentials{}
	// buildFailures are the configurations that failed to build lately (see
	// failureMemory).
	buildFailures = map[configKey]buildFailure{}
	// accountsMu guards callerAccounts.
	accountsMu sync.Mutex
	// callerAccounts are the account IDs callerAccount found, by the
	// credentials they are of.
	callerAccounts = map[*aws.CredentialsCache]string{}
	// testConfig, when set, is the configuration of every service, and its
	// credentials the base credentials. Tests set it to fake the API.
	testConfig *aws.Config
)

// cacheKey returns the key of the service's configuration, from its
// arguments.
func (s *Service) cacheKey() configKey {
	return configKey(settingsOf(s.Settings()))
}

// sdkConfig returns the SDK configuration of the service's account
// and region, built the first time it is asked for and shared after that:
// the services that ask while it is built wait for it, while those of
// other accounts and regions build their own at the same time.
// The credentials are resolved, and the role assumed, while it is built,
// so that a configuration it returns works. A configuration that failed to
// build fails again at once for a while (see failureMemory), so that the
// other services of an import don't each resolve the credentials again.
func (s *Service) sdkConfig() (aws.Config, error) {
	if testConfig != nil {
		return *testConfig, nil
	}
	key := s.cacheKey()
	if cfg, ok, err := cachedConfig(key); ok {
		return cfg, err
	}
	built, err, _ := configFlight.Do(key.flightKey(), func() (any, error) {
		// Another flight of key may have ended since the cache was read.
		if cfg, ok, err := cachedConfig(key); ok {
			return cfg, err
		}
		cfg, base, err := s.buildConfig(key)
		configMu.Lock()
		defer configMu.Unlock()
		if err != nil {
			buildFailures[key] = buildFailure{err: err, at: time.Now()}
			return aws.Config{}, err
		}
		delete(buildFailures, key)
		configs[key] = cfg
		baseProviders[key] = base
		return cfg, nil
	})
	if err != nil {
		return aws.Config{}, err
	}
	return built.(aws.Config), nil
}

// cachedConfig returns the configuration of key built already, or the
// error it failed to build with lately (see failureMemory), and whether
// there was either.
func cachedConfig(key configKey) (aws.Config, bool, error) {
	configMu.Lock()
	defer configMu.Unlock()
	if cfg, ok := configs[key]; ok {
		return cfg, true, nil
	}
	if failed, ok := buildFailures[key]; ok && time.Since(failed.at) < failureMemory {
		return aws.Config{}, true, failed.err
	}
	return aws.Config{}, false, nil
}

// buildConfig loads the configuration of key and gives it the credentials
// of its profile and role: those another region resolved already, or else
// it resolves them and, with a role, assumes it, and keeps them for the
// other regions. It returns the configuration and its credentials from
// before any role. It never sets an environment variable: the region and
// the profile are given to the SDK directly.
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

	resolved, err := resolveCredentials(ctx, credentialsKey{profile: key.profile, roleARN: key.roleARN}, cfg)
	if err != nil {
		return aws.Config{}, nil, err
	}
	cfg.Credentials = resolved.credentials
	return cfg, resolved.base, nil
}

// resolveCredentials returns the credentials of shared: those another
// region resolved already, or else it resolves those cfg loaded and, with
// a role, assumes it, and keeps them for the other regions. Credentials
// that fail to resolve aren't kept.
func resolveCredentials(ctx context.Context, shared credentialsKey, cfg aws.Config) (sharedCredentials, error) {
	if resolved, ok := keptCredentials(shared); ok {
		return resolved, nil
	}
	resolved, err, _ := credentialsFlight.Do(shared.flightKey(), func() (any, error) {
		// Another flight of shared may have ended since they were read.
		if resolved, ok := keptCredentials(shared); ok {
			return resolved, nil
		}
		resolved, err := newCredentials(ctx, shared, cfg)
		if err != nil {
			return nil, err
		}
		configMu.Lock()
		resolvedCredentials[shared] = resolved
		configMu.Unlock()
		return resolved, nil
	})
	if err != nil {
		return sharedCredentials{}, err
	}
	return resolved.(sharedCredentials), nil
}

// keptCredentials returns the credentials of shared resolved already, and
// whether there are any.
func keptCredentials(shared credentialsKey) (sharedCredentials, bool) {
	configMu.Lock()
	defer configMu.Unlock()
	resolved, ok := resolvedCredentials[shared]
	return resolved, ok
}

// newCredentials resolves the credentials cfg loaded and, when shared has
// a role, assumes it.
func newCredentials(ctx context.Context, shared credentialsKey, cfg aws.Config) (sharedCredentials, error) {
	if cfg.Credentials == nil {
		return sharedCredentials{}, errors.New("no AWS credentials found")
	}
	baseProvider := cfg.Credentials
	if _, ok := baseProvider.(*aws.CredentialsCache); !ok {
		baseProvider = aws.NewCredentialsCache(baseProvider, func(o *aws.CredentialsCacheOptions) {
			o.ExpiryWindow = credentialsExpiryWindow
		})
	}
	cfg.Credentials = baseProvider
	if _, err := baseProvider.Retrieve(ctx); err != nil {
		return sharedCredentials{}, err
	}

	if shared.roleARN != "" {
		role := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), shared.roleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = roleSessionName
		})
		cfg.Credentials = aws.NewCredentialsCache(role)
		if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
			return sharedCredentials{}, fmt.Errorf("assume %s: %w", shared.roleARN, err)
		}
	}
	return sharedCredentials{base: baseProvider, credentials: cfg.Credentials}, nil
}

// configOptions are the options the configuration of key loads with.
func (s *Service) configOptions(key configKey) []func(*config.LoadOptions) error {
	options := []func(*config.LoadOptions) error{
		// A profile's role that needs MFA asks for the code on the
		// terminal.
		config.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
			o.TokenProvider = stscreds.StdinTokenProvider
		}),
		// Credentials that expire are refreshed credentialsExpiryWindow
		// before they do, so that those Terraform gets have that long left.
		config.WithCredentialsCacheOptions(func(o *aws.CredentialsCacheOptions) {
			o.ExpiryWindow = credentialsExpiryWindow
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
		// Headers only: bodies can hold secrets. The headers hold the
		// request's signature and session token, so those are redacted.
		options = append(options,
			config.WithClientLogMode(aws.LogRequest|aws.LogResponse|aws.LogRetries),
			config.WithLogger(redactingLogger{logger: logging.NewStandardLogger(os.Stderr)}),
		)
	}
	return options
}

// callerAccount returns the ID of the account cfg's credentials are of. It
// asks STS once for the credentials of a configuration sdkConfig built,
// which every region of one profile and role shares, and remembers the
// answer: the scope of an import, the default selection and the listers
// that need the account then don't each ask again.
func (s *Service) callerAccount(cfg aws.Config) (*string, error) {
	cache, cached := cfg.Credentials.(*aws.CredentialsCache)
	cached = cached && testConfig == nil
	if cached {
		accountsMu.Lock()
		account, ok := callerAccounts[cache]
		accountsMu.Unlock()
		if ok {
			return aws.String(account), nil
		}
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(s.Context(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	if cached && identity.Account != nil {
		accountsMu.Lock()
		callerAccounts[cache] = *identity.Account
		accountsMu.Unlock()
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

// redactingLogger is the SDK's logger with --verbose: it logs what logger
// would, with the credentials in the requests and responses the SDK logs
// redacted (see logging.RedactHTTP).
type redactingLogger struct {
	logger logging.Logger
}

// Logf logs the formatted entry, redacted.
func (l redactingLogger) Logf(classification logging.Classification, format string, v ...any) {
	l.logger.Logf(classification, "%s", ihlogging.RedactHTTP(fmt.Sprintf(format, v...)))
}
