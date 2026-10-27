// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// resetSDKConfigs forgets the configurations, credentials, failures and
// accounts built so far, now and when the test ends.
func resetSDKConfigs(t *testing.T) {
	t.Helper()
	reset := func() {
		configMu.Lock()
		configs = map[configKey]aws.Config{}
		baseProviders = map[configKey]aws.CredentialsProvider{}
		resolvedCredentials = map[credentialsKey]sharedCredentials{}
		buildFailures = map[configKey]buildFailure{}
		configMu.Unlock()
		accountsMu.Lock()
		callerAccounts = map[*aws.CredentialsCache]string{}
		accountsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// One import covers global services (region aws-global) and then each
// --regions region; each must list with its own region.
func TestGenerateConfigPerRegion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	resetSDKConfigs(t)

	for _, region := range []string{GlobalPseudoRegion, "us-east-1", GlobalPseudoRegion} {
		s := &Service{}
		s.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": region, "profile": ""}})

		cfg, err := s.sdkConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Region != region {
			t.Errorf("config for %s has region %q", region, cfg.Region)
		}
	}
}

// With credentials only in the environment, as from OIDC in CI, there is no
// shared config file: the default --profile must still work.
func TestGenerateConfigWithoutSharedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "missing-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing-credentials"))
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	resetSDKConfigs(t)

	s := &Service{}
	s.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "us-east-1", "profile": "default"}})
	if _, err := s.sdkConfig(); err != nil {
		t.Errorf("want the environment's credentials used, got %v", err)
	}
}

// Terraform gets the credentials a profile resolves to through its own
// environment (TerraformEnv), not the process's.
func TestTerraformEnv(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"config":      "[profile prod]\nregion = eu-west-2\n",
		"credentials": "[prod]\naws_access_key_id = AKIDPROD\naws_secret_access_key = prod-secret\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION"} {
		t.Setenv(k, "")
	}
	resetSDKConfigs(t)
	p := &Provider{region: "eu-west-2", profile: "prod"}

	env, err := p.TerraformEnv(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if env["AWS_ACCESS_KEY_ID"] != "AKIDPROD" || env["AWS_SECRET_ACCESS_KEY"] != "prod-secret" || env["AWS_SESSION_TOKEN"] != "" {
		t.Errorf("env: %v", env)
	}
	if profile, ok := env["AWS_PROFILE"]; !ok || profile != "" {
		t.Errorf("AWS_PROFILE: %q, %t; want it empty, so that Terraform uses the keys", profile, ok)
	}
	if region, ok := env["AWS_REGION"]; ok {
		t.Errorf("AWS_REGION: %q; the provider block names the region", region)
	}

	// Without --regions, the region the profile resolves to.
	defaultRegion := &Provider{}
	if err := defaultRegion.Init(t.Context(), []string{ProfileRegion, "prod"}); err != nil {
		t.Fatal(err)
	}
	env, err = defaultRegion.TerraformEnv(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if env["AWS_REGION"] != "eu-west-2" {
		t.Errorf("AWS_REGION: %q, want the profile's eu-west-2", env["AWS_REGION"])
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_PROFILE") != "" {
		t.Error("the process environment changed")
	}
}

// useEnvironmentCredentials makes the SDK find only the credentials in the
// environment, keys when keyID is set and none otherwise, and no shared
// files or instance metadata.
func useEnvironmentCredentials(t *testing.T, keyID string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "missing-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing-credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", keyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", keyID)
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
}

// serviceIn returns a service of region, with the default profile.
func serviceIn(region string) *Service {
	s := &Service{}
	s.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": region, "profile": ""}})
	return s
}

// The regions of one profile and role share its credentials, resolved
// once: a role that needs MFA asks for the code once, and a role is assumed
// once, whatever the number of regions.
func TestSDKConfigSharesCredentialsAcrossRegions(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	resetSDKConfigs(t)

	first, err := serviceIn("eu-west-1").sdkConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := serviceIn("us-east-1").sdkConfig()
	if err != nil {
		t.Fatal(err)
	}

	if first.Region != "eu-west-1" || second.Region != "us-east-1" {
		t.Errorf("regions %q and %q", first.Region, second.Region)
	}
	if first.Credentials != second.Credentials {
		t.Error("each region resolved credentials of its own")
	}
}

// A configuration that failed to build fails again at once, without
// resolving the credentials again, until failureMemory has passed.
func TestSDKConfigRemembersFailures(t *testing.T) {
	useEnvironmentCredentials(t, "")
	resetSDKConfigs(t)
	s := serviceIn("eu-west-1")

	_, first := s.sdkConfig()
	if first == nil {
		t.Fatal("want an error without credentials")
	}
	// Credentials appear, but the failure is remembered.
	useEnvironmentCredentials(t, "test")
	if _, err := s.sdkConfig(); err == nil || err.Error() != first.Error() {
		t.Errorf("got %v, want the remembered %v", err, first)
	}

	configMu.Lock()
	failed := buildFailures[s.cacheKey()]
	failed.at = time.Now().Add(-2 * failureMemory)
	buildFailures[s.cacheKey()] = failed
	configMu.Unlock()
	if _, err := s.sdkConfig(); err != nil {
		t.Errorf("once the failure is old, want the configuration built again, got %v", err)
	}
}

// recordAssumeRole is STS's answer to AssumeRole.
const recordAssumeRole = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>` +
	`<Credentials><AccessKeyId>ASIA</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>` +
	`<AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/role/infraharvest</Arn><AssumedRoleId>AROA:infraharvest</AssumedRoleId></AssumedRoleUser>` +
	`</AssumeRoleResult></AssumeRoleResponse>`

// The configurations of different accounts build at once, each assuming
// its role while the other does; the services that ask for one that is
// being built wait for it, so that it is built, and its role assumed,
// once.
func TestSDKConfigBuildsEachKeyOnceAndKeysAtOnce(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	resetSDKConfigs(t)
	const roleA, roleB = "arn:aws:iam::111111111111:role/a", "arn:aws:iam::222222222222:role/b"
	at := newOverlap(2)
	var mu sync.Mutex
	assumed := map[string]int{}
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("Action") != "AssumeRole" {
			t.Errorf("unexpected STS call %v (%v)", r.Form, err)
		}
		mu.Lock()
		assumed[r.Form.Get("RoleArn")]++
		mu.Unlock()
		at.wait()
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, recordAssumeRole)
	}))
	t.Cleanup(sts.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", sts.URL)
	inRole := func(role string) *Service {
		s := &Service{}
		s.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "eu-west-1", "profile": "", "role_arn": role}})
		return s
	}
	services := []*Service{inRole(roleA), inRole(roleA), inRole(roleB), inRole(roleA)}

	built := make([]aws.Config, len(services))
	var wg sync.WaitGroup
	for i, s := range services {
		wg.Go(func() {
			var err error
			if built[i], err = s.sdkConfig(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	at.check(t)
	if assumed[roleA] != 1 || assumed[roleB] != 1 {
		t.Errorf("roles assumed %v, want each once", assumed)
	}
	if built[0].HTTPClient != built[1].HTTPClient || built[0].HTTPClient != built[3].HTTPClient {
		t.Error("one key's configuration was built more than once")
	}
	if built[0].HTTPClient == built[2].HTTPClient {
		t.Error("two keys share one configuration")
	}
}

// callerAccount asks STS once for the credentials of a configuration, and
// remembers the account for every region that shares them.
func TestCallerAccountIsAskedOnce(t *testing.T) {
	resetSDKConfigs(t)
	calls := 0
	api := &fakeAPI{handle: func(call apiCall) string {
		if call.Op != "GetCallerIdentity" {
			return recordUnexpected(t, call)
		}
		calls++
		return recordQuery(call.Op, "https://sts.amazonaws.com/doc/2011-06-15/", `<Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/dev</Arn><UserId>AIDA</UserId>`)
	}}
	credentials := aws.NewCredentialsCache(aws.AnonymousCredentials{})
	s := &Service{}

	for _, region := range []string{"eu-west-1", "us-east-1", "eu-west-1"} {
		cfg := aws.Config{Region: region, Credentials: credentials, HTTPClient: api, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
		account, err := s.callerAccount(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(account) != "123456789012" {
			t.Errorf("%s: account %q", region, aws.ToString(account))
		}
	}
	if calls != 1 {
		t.Errorf("GetCallerIdentity called %d times, want once", calls)
	}
}

// What the provider passes each service is what picks its SDK
// configuration and region.
func TestServiceReadsTheProviderArgs(t *testing.T) {
	p := &Provider{region: "eu-west-2", profile: "audit", roleARN: "arn:aws:iam::111122223333:role/reader"}
	s := &Service{}
	s.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	want := configKey{region: "eu-west-2", profile: "audit", roleARN: "arn:aws:iam::111122223333:role/reader"}
	if got := s.cacheKey(); got != want {
		t.Errorf("cacheKey: got %+v, want %+v", got, want)
	}
	if got := regionSetting(s.Settings()); got != "eu-west-2" {
		t.Errorf("regionSetting: got %q", got)
	}
	if got := regionSetting(map[string]interface{}{regionKey: 1}); got != "" {
		t.Errorf("regionSetting of another type: got %q, want none", got)
	}
}

// The settings a service gets come back as they went in, and those of
// another type as none.
func TestServiceSettingsRoundTrip(t *testing.T) {
	want := serviceSettings{region: "eu-west-2", profile: "audit", roleARN: "arn:aws:iam::111122223333:role/reader"}
	args := want.args()
	if got := settingsOf(args); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if args[skipRegionValidationKey] != true {
		t.Errorf("skip_region_validation: got %v", args[skipRegionValidationKey])
	}
	if got := settingsOf(map[string]interface{}{regionKey: 1, profileKey: true}); got != (serviceSettings{}) {
		t.Errorf("settings of other types: got %+v", got)
	}
}
