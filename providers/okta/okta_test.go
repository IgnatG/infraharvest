// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package okta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

func decode(t *testing.T, data string, v interface{}) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), v); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
}

// listed is the part of a resource a lister decides.
type listed struct {
	ID, Name, Type string
	Attributes     map[string]string
}

func summarize(resources []terraformutils.Resource) []listed {
	var out []listed
	for _, r := range resources {
		out = append(out, listed{r.InstanceState.ID, r.RawName, r.InstanceInfo.Type, r.InstanceState.Attributes})
	}
	return out
}

func TestAppsBySignOnMode(t *testing.T) {
	var apps []okta.ListApplications200ResponseInner
	decode(t, `[
		{"id":"0oa1","name":"template_swa","label":"Wiki","signOnMode":"AUTO_LOGIN"},
		{"id":"0oa2","name":"template_basic_auth","label":"Intranet","signOnMode":"BASIC_AUTH"},
		{"id":"0oa3","name":"template_sps","label":"Vault","signOnMode":"SECURE_PASSWORD_STORE"},
		{"id":"0oa4","name":"template_swa3field","label":"Bank","signOnMode":"BROWSER_PLUGIN","settings":{}},
		{"id":"0oa5","name":"template_swa","label":"Mail","signOnMode":"BROWSER_PLUGIN","settings":{}},
		{"id":"0oa6","name":"saasure","label":"Okta Admin Console","signOnMode":"AUTO_LOGIN"},
		{"id":"0oa7","name":"bookmark","label":"Docs","signOnMode":"BOOKMARK","settings":{"app":{"url":"https://example.com"}}}
	]`, &apps)

	all := supportedApps(apps)
	if len(all) != 6 {
		t.Fatalf("supportedApps kept %d apps, want 6 (saasure left out): %+v", len(all), all)
	}

	got := summarize(AppAutoLoginGenerator{}.createResources(appsWithSignOnMode(all, "AUTO_LOGIN")))
	want := []listed{{"0oa1", "oa1-template-swa", "okta_app_auto_login", map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("auto login: got %+v, want %+v", got, want)
	}

	got = summarize(AppBasicAuthGenerator{}.createResources(appsWithSignOnMode(all, "BASIC_AUTH")))
	want = []listed{{"0oa2", "oa2-template-basic-auth", "okta_app_basic_auth", map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("basic auth: got %+v, want %+v", got, want)
	}

	got = summarize(AppSecurePasswordStoreGenerator{}.createResources(appsWithSignOnMode(all, "SECURE_PASSWORD_STORE")))
	want = []listed{{"0oa3", "oa3-template-sps", "okta_app_secure_password_store", map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secure password store: got %+v, want %+v", got, want)
	}

	var threeField []oktaApp
	for _, app := range appsWithSignOnMode(all, "BROWSER_PLUGIN") {
		if app.Name == "template_swa3field" {
			threeField = append(threeField, app)
		}
	}
	got = summarize(AppThreeFieldGenerator{}.createResources(threeField))
	want = []listed{{"0oa4", "oa4-template-swa3field", "okta_app_three_field", map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("three field: got %+v, want %+v", got, want)
	}
}

func TestToOktaPolicies(t *testing.T) {
	var items []okta.ListPolicies200ResponseInner
	decode(t, `[
		{"id":"00p1","name":"Default Policy","type":"MFA_ENROLL"},
		{"id":"00p2","name":"Strict","type":"PASSWORD"},
		{"id":"00p3","name":"Unknown","type":"NOT_A_POLICY_TYPE"}
	]`, &items)

	got := toOktaPolicies(items)
	want := []oktaPolicy{{ID: "00p1", Name: "Default Policy"}, {ID: "00p2", Name: "Strict"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	resources := summarize(MFAPolicyGenerator{}.createResources(got[:1]))
	if resources[0].Type != "okta_policy_mfa_default" || resources[0].Name != "policy_mfa_default-policy" {
		t.Errorf("default MFA policy listed as %+v", resources[0])
	}
}

func TestUserSchemaPropertyNames(t *testing.T) {
	var schema okta.UserSchema
	decode(t, `{
		"id":"https://example.okta.com/meta/schemas/user/osc1",
		"definitions":{
			"base":{"id":"#base","type":"object","properties":{
				"login":{"title":"Username","type":"string"},
				"userName":{"title":"App username","type":"string"}
			}},
			"custom":{"id":"#custom","type":"object","properties":{
				"team":{"title":"Team","type":"string"}
			}}
		}
	}`, &schema)

	custom, base, err := userSchemaPropertyNames(&schema)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(custom, []string{"team"}) {
		t.Errorf("custom properties = %v", custom)
	}
	if !reflect.DeepEqual(base, []string{"login", "userName"}) {
		t.Errorf("base properties = %v", base)
	}

	resources, err := AppUserSchemaPropertyGenerator{}.createResources(&schema, "0oa1")
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(resources)
	want := []listed{
		{"team", "oa1_property_team", "okta_app_user_schema_property", map[string]string{"app_id": "0oa1", "index": "team"}},
		{"login", "oa1_property_login", "okta_app_user_base_schema_property", map[string]string{"app_id": "0oa1", "index": "login"}},
		{"userName", "oa1_property_username", "okta_app_user_base_schema_property", map[string]string{"app_id": "0oa1", "index": "userName"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	if custom, base, err := userSchemaPropertyNames(&okta.UserSchema{}); err != nil || custom != nil || base != nil {
		t.Errorf("empty schema: %v %v %v", custom, base, err)
	}
}

func TestUserType(t *testing.T) {
	var userTypes []okta.UserType
	decode(t, `[{"id":"oty1","name":"user","displayName":"User","_links":{"schema":{"href":"https://example.okta.com/api/v1/meta/schemas/user/osc1"}}}]`, &userTypes)

	if got := userTypeName(userTypes[0]); got != "user" {
		t.Errorf("userTypeName = %q", got)
	}
	if got := getUserTypeSchemaID(userTypes[0]); got != "osc1" {
		t.Errorf("getUserTypeSchemaID = %q", got)
	}
	got := summarize(UserTypeGenerator{}.createResources(userTypes))
	want := []listed{{"oty1", "usertype_user", "okta_user_type", map[string]string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAuthorizationServerPolicy(t *testing.T) {
	var policies []okta.AuthorizationServerPolicy
	decode(t, `[{"id":"00p1","name":"Default Policy","type":"OAUTH_AUTHORIZATION_POLICY","status":"ACTIVE","priority":1}]`, &policies)

	got := summarize(AuthorizationServerPolicyGenerator{}.createResources(policies, "aus1", "default"))
	want := []listed{{"00p1", "auth-server-default-policy-default-policy", "okta_auth_server_policy", map[string]string{"auth_server_id": "aus1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestNextLink(t *testing.T) {
	header := http.Header{}
	header.Add("Link", `<https://example.okta.com/api/v1/policies/00p1/rules?limit=1>; rel="self"`)
	header.Add("Link", `<https://example.okta.com/api/v1/policies/00p1/rules?after=r1&limit=1>; rel="next"`)
	if got := nextLink(header); got != "https://example.okta.com/api/v1/policies/00p1/rules?after=r1&limit=1" {
		t.Errorf("nextLink = %q", got)
	}

	header = http.Header{}
	header.Set("Link", `<https://example.okta.com/a>; rel="self", <https://example.okta.com/b>; rel=next`)
	if got := nextLink(header); got != "https://example.okta.com/b" {
		t.Errorf("nextLink = %q", got)
	}

	if got := nextLink(http.Header{}); got != "" {
		t.Errorf("nextLink without Link header = %q", got)
	}
}

func TestRawListFollowsPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "SSWS token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/api/v1/policies/00p1/rules" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			// A next link on another host must still be fetched from the org.
			w.Header().Add("Link", `<https://elsewhere.example.com/api/v1/policies/00p1/rules?after=r1>; rel="next"`)
			fmt.Fprint(w, `[{"id":"r1","name":"First","type":"MFA_ENROLL"}]`)
			return
		}
		fmt.Fprint(w, `[{"id":"r2","name":"Second","type":"MFA_ENROLL"}]`)
	}))
	defer server.Close()

	orgURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &rawClient{orgURL: orgURL, token: "token", httpClient: server.Client()}

	rules, err := listPolicyRules(context.Background(), client, "00p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []oktaPolicyRule{{ID: "r1", Name: "First"}, {ID: "r2", Name: "Second"}}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("got %+v, want %+v", rules, want)
	}

	got := summarize(MFAPolicyRuleGenerator{}.createResources(rules, "00p1", "Default Policy"))
	if got[0].ID != "r1" || got[0].Name != "policyrule_mfa_default-policy-first" || got[0].Type != "okta_policy_rule_mfa" ||
		!reflect.DeepEqual(got[0].Attributes, map[string]string{"policy_id": "00p1"}) {
		t.Errorf("rule listed as %+v", got[0])
	}

	if _, err := rawList[oktaPolicyRule](context.Background(), client, "/api/v1/missing"); err == nil {
		t.Error("rawList of a missing path succeeded")
	}
}

func TestFactorResources(t *testing.T) {
	factors := []orgFactor{
		{ID: "okta_otp", FactorType: "token:software:totp", Status: "ACTIVE"},
		{ID: "hotp", FactorType: "token:hotp", Status: "ACTIVE"},
		{ID: "sms", FactorType: "sms", Status: "INACTIVE"},
	}
	if !hasActiveHotpFactor(factors) {
		t.Fatal("hasActiveHotpFactor = false")
	}
	profiles := []hotpFactorProfile{{ID: "fhp1", Name: "Yubikey"}}

	got := summarize(FactorGenerator{}.createResources(factors, profiles))
	if len(got) != 3 {
		t.Fatalf("got %d resources, want 3: %+v", len(got), got)
	}
	if got[0].ID != "okta_otp" || got[0].Type != "okta_factor" || got[0].Attributes["provider_id"] != "okta_otp" {
		t.Errorf("factor listed as %+v", got[0])
	}
	if got[1].ID != "hotp" || got[1].Type != "okta_factor" {
		t.Errorf("factor listed as %+v", got[1])
	}
	if got[2].ID != "fhp1" || got[2].Type != "okta_factor_totp" {
		t.Errorf("HOTP profile listed as %+v", got[2])
	}
}

// rateLimitedServer answers the first limited requests with 429 (with
// reset as X-Rate-Limit-Reset when it is set) and the rest with one rule.
func rateLimitedServer(t *testing.T, limited int32, reset string) (*rawClient, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= limited {
			if reset != "" {
				w.Header().Set("X-Rate-Limit-Reset", reset)
			}
			http.Error(w, `{"errorCode":"E0000047"}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"r1","name":"First"}]`)
	}))
	t.Cleanup(server.Close)
	orgURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &rawClient{orgURL: orgURL, token: "token", httpClient: server.Client(), retryBackoff: time.Millisecond}, &requests
}

func TestRawRetriesRateLimitedRequest(t *testing.T) {
	// The reset time is already past, so the retry waits the short backoff.
	client, requests := rateLimitedServer(t, 1, strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))

	rules, err := listPolicyRules(context.Background(), client, "00p1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []oktaPolicyRule{{ID: "r1", Name: "First"}}; !reflect.DeepEqual(rules, want) {
		t.Errorf("got %+v, want %+v", rules, want)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
}

func TestRawGivesUpOnRepeatedRateLimit(t *testing.T) {
	client, requests := rateLimitedServer(t, 1000, "")

	_, err := listPolicyRules(context.Background(), client, "00p1")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want the 429 error", err)
	}
	if got := requests.Load(); got != rateLimitAttempts {
		t.Errorf("made %d requests, want %d", got, rateLimitAttempts)
	}
}

func TestRawRateLimitWaitHonoursContext(t *testing.T) {
	client, requests := rateLimitedServer(t, 1000, "")
	client.retryBackoff = time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := listPolicyRules(ctx, client, "00p1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	client := &rawClient{retryBackoff: 2 * time.Second}
	header := func(reset string) http.Header {
		h := http.Header{}
		if reset != "" {
			h.Set("X-Rate-Limit-Reset", reset)
		}
		return h
	}

	for _, tc := range []struct {
		reset string
		want  time.Duration
	}{
		{"1700000010", 10 * time.Second}, // until the reset
		{"", 2 * time.Second},            // no header: the backoff
		{"soon", 2 * time.Second},        // invalid header: the backoff
		{"1699999990", 2 * time.Second},  // reset already past: the backoff
		{"1700086400", maxRateLimitWait}, // a day away: capped
	} {
		if got := client.rateLimitWait(header(tc.reset), now); got != tc.want {
			t.Errorf("reset %q: wait %v, want %v", tc.reset, got, tc.want)
		}
	}

	if got := (&rawClient{}).rateLimitWait(http.Header{}, now); got != defaultRetryBackoff {
		t.Errorf("zero backoff: wait %v, want %v", got, defaultRetryBackoff)
	}
}
