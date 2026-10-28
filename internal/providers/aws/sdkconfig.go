// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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

// serviceSettings are the settings each service gets (see
// Provider.settings). They pick the service's SDK configuration (see
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
// state, and the SDK configuration its settings pick (the region, the
// profile and the role, see Provider.settings).
type Service struct {
	harvest.ListerOf[serviceSettings]
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

// wantsAny reports whether the import may keep resources of any of types:
// a call that lists several types is skipped only when it wants none.
func (s *Service) wantsAny(types ...string) bool {
	return slices.ContainsFunc(types, s.wants)
}

// configKey picks one SDK configuration: the services of one account and
// region share it.
type configKey struct {
	region, profile, roleARN string
}

// flightKey is the key as runConfigs.flight takes it.
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

// runConfigs are the configurations of one run, which every service of
// the run shares: each holds the run's limits (see Service.buildConfig),
// so another run, with a rate of its own, builds its own (see
// configsOf).
type runConfigs struct {
	// mu guards configs, baseProviders and buildFailures. It isn't held
	// while a configuration is built, so that the configurations of
	// different accounts and regions build at once.
	mu sync.Mutex
	// flight builds each configuration once at a time: the first service
	// to need one builds it, and the others of its key wait for it and
	// share the answer.
	flight singleflight.Group
	// configs are the configurations built so far.
	configs map[configKey]aws.Config
	// baseProviders are the credentials of each configuration before any
	// role is assumed, which Terraform gets (see baseCredentials).
	baseProviders map[configKey]aws.CredentialsProvider
	// buildFailures are the configurations that failed to build lately (see
	// failureMemory).
	buildFailures map[configKey]buildFailure
	// fixed, when set, is the configuration of every service of the run,
	// and its credentials the base credentials: nothing is built or
	// resolved. Tests set it, on a run of their own, to fake the API.
	fixed *aws.Config

	// credentialsMu guards credentials.
	credentialsMu sync.Mutex
	// credentialsFlight resolves the credentials of each profile and role
	// once at a time, as resolving them may prompt for an MFA code: the
	// first region to need them resolves them, and the others wait for
	// them and share the answer.
	credentialsFlight singleflight.Group
	// credentials are the credentials the run resolved so far, by profile
	// and role, which the configuration of every region of them shares: a
	// profile's role that needs MFA asks for the code once, not once per
	// region, and a role is assumed once. They are the run's, not the
	// process's: they depend on the environment, the shared files and the
	// session a role's MFA code opened, any of which can change before the
	// next run, which resolves them again.
	credentials map[credentialsKey]sharedCredentials

	// accountsMu guards callerAccounts.
	accountsMu sync.Mutex
	// callerAccounts are the account IDs callerAccount found, by the
	// credentials they are of.
	callerAccounts map[*aws.CredentialsCache]string

	// scansMu guards stackScans.
	scansMu sync.Mutex
	// stackScans are the regions the run scanned lately (see
	// stackScanMemory).
	stackScans map[stackScanKey]stackScan

	// explorerMu guards explorerRegions.
	explorerMu sync.Mutex
	// explorerRegions are what Resource Explorer says about each account
	// and region the run lists, read once for all of its services (see
	// explorerFor).
	explorerRegions map[configKey]*explorerRegion
	// explorerNow is the clock an index's age is measured by. Tests set
	// it, on a run of their own.
	explorerNow func() time.Time
}

// runConfigsKey is the key of the run's configurations kept with its
// limiter.
type runConfigsKey struct{}

// configsOf returns the configurations of the run whose limiter is l,
// made the first time it is asked for: they are kept with the limiter, so
// they last as long as the run.
func configsOf(l *ratelimit.Limiter) *runConfigs {
	return ratelimit.Kept(l, runConfigsKey{}, func() *runConfigs {
		return &runConfigs{
			configs:         map[configKey]aws.Config{},
			baseProviders:   map[configKey]aws.CredentialsProvider{},
			buildFailures:   map[configKey]buildFailure{},
			credentials:     map[credentialsKey]sharedCredentials{},
			callerAccounts:  map[*aws.CredentialsCache]string{},
			stackScans:      map[stackScanKey]stackScan{},
			explorerRegions: map[configKey]*explorerRegion{},
			explorerNow:     time.Now,
		}
	})
}

// cacheKey returns the key of the service's configuration, from its
// settings.
func (s *Service) cacheKey() configKey {
	return configKey(s.Settings())
}

// configs returns the configurations of the service's run: those kept
// with the limiter of its context (see ratelimit.FromContext).
func (s *Service) configs() *runConfigs {
	return configsOf(ratelimit.FromContext(s.Context()))
}

// sdkConfig returns the SDK configuration of the service's account
// and region, built the first time its run asks for it and shared by the
// run after that (see runConfigs):
// the services that ask while it is built wait for it, while those of
// other accounts and regions build their own at the same time.
// The credentials are resolved, and the role assumed, while it is built,
// so that a configuration it returns works. A configuration that failed to
// build fails again at once for a while (see failureMemory), so that the
// other services of an import don't each resolve the credentials again.
func (s *Service) sdkConfig() (aws.Config, error) {
	key, run := s.cacheKey(), s.configs()
	if run.fixed != nil {
		return *run.fixed, nil
	}
	if cfg, ok, err := run.cached(key); ok {
		return cfg, err
	}
	built, err, _ := run.flight.Do(key.flightKey(), func() (any, error) {
		// Another flight of key may have ended since the cache was read.
		if cfg, ok, err := run.cached(key); ok {
			return cfg, err
		}
		cfg, base, err := s.buildConfig(run, key)
		run.mu.Lock()
		defer run.mu.Unlock()
		if err != nil {
			run.buildFailures[key] = buildFailure{err: err, at: time.Now()}
			return aws.Config{}, err
		}
		delete(run.buildFailures, key)
		run.configs[key] = cfg
		run.baseProviders[key] = base
		return cfg, nil
	})
	if err != nil {
		return aws.Config{}, err
	}
	return built.(aws.Config), nil
}

// cached returns the configuration of key built already, or the error it
// failed to build with lately (see failureMemory), and whether there was
// either.
func (r *runConfigs) cached(key configKey) (aws.Config, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cfg, ok := r.configs[key]; ok {
		return cfg, true, nil
	}
	if failed, ok := r.buildFailures[key]; ok && time.Since(failed.at) < failureMemory {
		return aws.Config{}, true, failed.err
	}
	return aws.Config{}, false, nil
}

// buildConfig loads the configuration of key and gives it the credentials
// of its profile and role: those another region of run resolved already,
// or else it resolves them and, with a role, assumes it, and keeps them
// for the run's other regions. It returns the configuration and its credentials from
// before any role. It never sets an environment variable: the region and
// the profile are given to the SDK directly.
func (s *Service) buildConfig(run *runConfigs, key configKey) (aws.Config, aws.CredentialsProvider, error) {
	ctx := s.Context()
	cfg, err := config.LoadDefaultConfig(ctx, s.configOptions(key)...)
	if err != nil {
		return aws.Config{}, nil, err
	}

	// Each configuration, so each account and region, gets its own limit
	// on the requests to each API host, at the rate of the run's limiter.
	base := cfg.HTTPClient
	if base == nil {
		base = awshttp.NewBuildableClient()
	}
	cfg.HTTPClient = ratelimit.Like(ratelimit.FromContext(ctx)).Client(base)

	resolved, err := run.resolveCredentials(ctx, credentialsKey{profile: key.profile, roleARN: key.roleARN}, cfg)
	if err != nil {
		return aws.Config{}, nil, err
	}
	cfg.Credentials = resolved.credentials
	return cfg, resolved.base, nil
}

// resolveCredentials returns the credentials of shared: those another
// region of the run resolved already, or else it resolves those cfg
// loaded and, with a role, assumes it, and keeps them for the run's other
// regions. Credentials that fail to resolve aren't kept.
func (r *runConfigs) resolveCredentials(ctx context.Context, shared credentialsKey, cfg aws.Config) (sharedCredentials, error) {
	if resolved, ok := r.keptCredentials(shared); ok {
		return resolved, nil
	}
	resolved, err, _ := r.credentialsFlight.Do(shared.flightKey(), func() (any, error) {
		// Another flight of shared may have ended since they were read.
		if resolved, ok := r.keptCredentials(shared); ok {
			return resolved, nil
		}
		resolved, err := newCredentials(ctx, shared, cfg)
		if err != nil {
			return nil, err
		}
		r.credentialsMu.Lock()
		r.credentials[shared] = resolved
		r.credentialsMu.Unlock()
		return resolved, nil
	})
	if err != nil {
		return sharedCredentials{}, err
	}
	return resolved.(sharedCredentials), nil
}

// keptCredentials returns the credentials of shared the run resolved
// already, and whether there are any.
func (r *runConfigs) keptCredentials(shared credentialsKey) (sharedCredentials, bool) {
	r.credentialsMu.Lock()
	defer r.credentialsMu.Unlock()
	resolved, ok := r.credentials[shared]
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
// which every region of one profile and role shares, and the run
// remembers the answer: the scope of an import, the default selection and
// the listers that need the account then don't each ask again.
func (s *Service) callerAccount(cfg aws.Config) (*string, error) {
	run := s.configs()
	cache, cached := cfg.Credentials.(*aws.CredentialsCache)
	if cached {
		run.accountsMu.Lock()
		account, ok := run.callerAccounts[cache]
		run.accountsMu.Unlock()
		if ok {
			return aws.String(account), nil
		}
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(s.Context(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	if cached && identity.Account != nil {
		run.accountsMu.Lock()
		run.callerAccounts[cache] = *identity.Account
		run.accountsMu.Unlock()
	}
	return identity.Account, nil
}

// baseCredentials returns the credentials of the service's configuration
// from before any role is assumed, refreshed if they have expired.
func (s *Service) baseCredentials() (aws.Credentials, error) {
	run := s.configs()
	if run.fixed != nil {
		if run.fixed.Credentials == nil {
			return aws.Credentials{}, errors.New("the fixed configuration has no credentials")
		}
		return run.fixed.Credentials.Retrieve(s.Context())
	}
	if _, err := s.sdkConfig(); err != nil {
		return aws.Credentials{}, err
	}
	run.mu.Lock()
	provider := run.baseProviders[s.cacheKey()]
	run.mu.Unlock()
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
