// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/internal/ratelimit"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// newRun returns a context of a run of its own, which resolves the
// credentials and builds the configurations afresh, whatever other tests
// resolved and built.
func newRun(t *testing.T) context.Context {
	t.Helper()
	return ratelimit.NewContext(t.Context(), ratelimit.New(0))
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
	ctx := newRun(t)

	for _, region := range []string{GlobalPseudoRegion, "us-east-1", GlobalPseudoRegion} {
		s := &Service{}
		s.Begin(ctx, harvest.Settings{})
		s.SetSettings(serviceSettings{region: region})

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
	ctx := newRun(t)

	s := &Service{}
	s.Begin(ctx, harvest.Settings{})
	s.SetSettings(serviceSettings{region: "us-east-1", profile: "default"})
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
	ctx := newRun(t)
	p := &Provider{region: "eu-west-2", profile: "prod"}

	env, err := p.TerraformEnv(ctx)
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
	if err := defaultRegion.Init(ctx, []string{ProfileRegion, "prod"}); err != nil {
		t.Fatal(err)
	}
	env, err = defaultRegion.TerraformEnv(ctx)
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

// serviceIn returns a service of region, with the default profile, in
// the run of ctx.
func serviceIn(ctx context.Context, region string) *Service {
	s := &Service{}
	s.Begin(ctx, harvest.Settings{})
	s.SetSettings(serviceSettings{region: region})
	return s
}

// The regions of one profile and role share its credentials, resolved
// once: a role that needs MFA asks for the code once, and a role is assumed
// once, whatever the number of regions.
func TestSDKConfigSharesCredentialsAcrossRegions(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	ctx := newRun(t)

	first, err := serviceIn(ctx, "eu-west-1").sdkConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := serviceIn(ctx, "us-east-1").sdkConfig()
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
	ctx := newRun(t)
	s := serviceIn(ctx, "eu-west-1")

	_, first := s.sdkConfig()
	if first == nil {
		t.Fatal("want an error without credentials")
	}
	// Credentials appear, but the failure is remembered.
	useEnvironmentCredentials(t, "test")
	if _, err := s.sdkConfig(); err == nil || err.Error() != first.Error() {
		t.Errorf("got %v, want the remembered %v", err, first)
	}

	run := s.configs()
	run.mu.Lock()
	failed := run.buildFailures[s.cacheKey()]
	failed.at = time.Now().Add(-2 * failureMemory)
	run.buildFailures[s.cacheKey()] = failed
	run.mu.Unlock()
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
	ctx := newRun(t)
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
		s.Begin(ctx, harvest.Settings{})
		s.SetSettings(serviceSettings{region: "eu-west-1", roleARN: role})
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

// Two runs in one process, at once, each build the configuration of an
// account and region with their own run's rate, and its own credentials:
// neither takes the other's configuration, or its limit. The
// slow run's second request waits past its deadline; the fast run's
// doesn't wait. Run with -race.
func TestSDKConfigIsTheRunsOwn(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()

	runs := []*ratelimit.Limiter{ratelimit.New(1), ratelimit.New(1000)}
	built := make([]aws.Config, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Go(func() {
			s := serviceIn(ratelimit.NewContext(t.Context(), run), "eu-west-1")
			cfg, err := s.sdkConfig()
			if err != nil {
				t.Error(err)
			}
			built[i] = cfg
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if built[0].HTTPClient == built[1].HTTPClient {
		t.Fatal("the runs share one configuration")
	}
	if built[0].Credentials == built[1].Credentials {
		t.Error("the runs share credentials")
	}
	// waited sends two requests through cfg, the second with a deadline
	// shorter than a second, and reports whether it waited past it.
	waited := func(cfg aws.Config) bool {
		for i := range 2 {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			resp, err := cfg.HTTPClient.Do(req)
			cancel()
			if err != nil {
				return i == 1
			}
			resp.Body.Close()
		}
		return false
	}
	if !waited(built[0]) {
		t.Error("the run of 1 a second sent two requests at once")
	}
	if waited(built[1]) {
		t.Error("the run of 1000 a second waited: it has the other run's rate")
	}
}

// Each run resolves the credentials afresh: a run after the environment's
// credentials changed, as a long-lived process's next run, uses the new
// ones, not those an earlier run resolved.
func TestSDKConfigResolvesEachRunsCredentials(t *testing.T) {
	for _, keyID := range []string{"AKIDFIRST", "AKIDSECOND"} {
		useEnvironmentCredentials(t, keyID)
		got, err := serviceIn(newRun(t), "eu-west-1").baseCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if got.AccessKeyID != keyID {
			t.Errorf("a run with %s in the environment got the credentials of %s", keyID, got.AccessKeyID)
		}
	}
}

// The provider's own calls use the configuration of the run Init was
// given, even with a context that carries no limiter, as Terraform's
// credentials are asked for.
func TestProviderCallsUseTheRunsConfig(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	ctx := ratelimit.NewContext(t.Context(), ratelimit.New(5))
	p := &Provider{}
	if err := p.Init(ctx, []string{"eu-west-1", ""}); err != nil {
		t.Fatal(err)
	}
	listed, err := serviceWithContext(ctx, p).sdkConfig()
	if err != nil {
		t.Fatal(err)
	}
	own, err := p.service(context.Background()).sdkConfig()
	if err != nil {
		t.Fatal(err)
	}
	if own.HTTPClient != listed.HTTPClient {
		t.Error("a call without the run's limiter built another configuration")
	}
}

// serviceWithContext returns a service of p's settings, listing with ctx,
// as the importer gives each service its listing's context.
func serviceWithContext(ctx context.Context, p *Provider) *Service {
	s := &Service{}
	s.SetSettings(p.settings())
	s.Begin(ctx, harvest.Settings{})
	return s
}

// callerAccount asks STS once for the credentials of a configuration, and
// remembers the account for every region that shares them.
func TestCallerAccountIsAskedOnce(t *testing.T) {
	t.Parallel()
	ctx := newRun(t)
	calls := 0
	api := fakeAPI(func(call apiCall) string {
		if call.Op != "GetCallerIdentity" {
			return recordUnexpected(t, call)
		}
		calls++
		return recordQuery(call.Op, "https://sts.amazonaws.com/doc/2011-06-15/", `<Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/dev</Arn><UserId>AIDA</UserId>`)
	})
	credentials := aws.NewCredentialsCache(aws.AnonymousCredentials{})
	s := &Service{}
	s.Begin(ctx, harvest.Settings{})

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

// Lister passes the service the provider's settings, which pick its SDK
// configuration and region: both the guard and the lister it guards get
// them.
func TestListerPassesTheSettings(t *testing.T) {
	t.Parallel()
	p := &Provider{region: "eu-west-2", profile: "audit", roleARN: "arn:aws:iam::111122223333:role/reader"}
	lister, err := p.Lister("sqs")
	if err != nil {
		t.Fatal(err)
	}
	guard := lister.(*guardedLister)
	want := configKey{region: "eu-west-2", profile: "audit", roleARN: "arn:aws:iam::111122223333:role/reader"}
	if got := configKey(guard.settings); got != want {
		t.Errorf("the guard's settings: got %+v, want %+v", got, want)
	}
	if got := guard.lister.(*SqsLister).cacheKey(); got != want {
		t.Errorf("the lister's cacheKey: got %+v, want %+v", got, want)
	}
}
