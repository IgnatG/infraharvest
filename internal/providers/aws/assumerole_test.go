// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Args adds the external ID and the session name only where set, so that
// a call without them keeps the args, and the inventory, it had; Init
// reads them back.
func TestAssumeRoleArgs(t *testing.T) {
	t.Parallel()
	const role = "arn:aws:iam::111122223333:role/reader"
	for _, tt := range []struct {
		role AssumeRole
		want []string
	}{
		{AssumeRole{ARN: role}, []string{"eu-west-2", "audit", role}},
		{AssumeRole{ARN: role, ExternalID: "ext-1"}, []string{"eu-west-2", "audit", role, "ext-1"}},
		{AssumeRole{ARN: role, SessionName: "ci-42"}, []string{"eu-west-2", "audit", role, "", "ci-42"}},
		{AssumeRole{ARN: role, ExternalID: "ext-1", SessionName: "ci-42"}, []string{"eu-west-2", "audit", role, "ext-1", "ci-42"}},
	} {
		args := tt.role.Args("eu-west-2", "audit")
		if !slices.Equal(args, tt.want) {
			t.Errorf("%+v: args %q, want %q", tt.role, args, tt.want)
		}
		p := &Provider{}
		if err := p.Init(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		want := serviceSettings{region: "eu-west-2", profile: "audit", roleARN: role, externalID: tt.role.ExternalID, sessionName: tt.role.SessionName}
		if p.settings() != want {
			t.Errorf("%+v: settings %+v, want %+v", tt.role, p.settings(), want)
		}
	}
}

// Check refuses what STS would.
func TestAssumeRoleCheck(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		role AssumeRole
		ok   bool
	}{
		{AssumeRole{}, true},
		{AssumeRole{ExternalID: "a1b2-c3:d4/e5", SessionName: "ci.run@42"}, true},
		{AssumeRole{SessionName: "x"}, false},
		{AssumeRole{SessionName: strings.Repeat("a", 65)}, false},
		{AssumeRole{SessionName: "has space"}, false},
		{AssumeRole{ExternalID: "bad id"}, false},
	} {
		if err := tt.role.Check(); (err == nil) != tt.ok {
			t.Errorf("%+v: error %v, want ok %v", tt.role, err, tt.ok)
		}
	}
}

// The provider block assumes the role with the external ID and the
// session name, where set, so that the roots' plans can assume it too.
func TestProviderBlockAssumesTheRoleWithItsExternalID(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_PROFILE", "")
	p := &Provider{}
	if err := p.Init(t.Context(), AssumeRole{ARN: "arn:aws:iam::111122223333:role/reader", ExternalID: "ext-1", SessionName: "ci-42"}.Args("eu-west-2", "default")); err != nil {
		t.Fatal(err)
	}
	role := p.ProviderBlock()["provider"].(map[string]interface{})["aws"].(map[string]interface{})["assume_role"].(map[string]interface{})
	if role["external_id"] != "ext-1" || role["session_name"] != "ci-42" {
		t.Errorf("assume_role: %v", role)
	}
}

// The listing assumes the role with the external ID and the session name,
// or the default session name; another external ID is other credentials.
func TestSDKConfigAssumesWithExternalID(t *testing.T) {
	useEnvironmentCredentials(t, "test")
	ctx := newRun(t)
	var asked []string
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		asked = append(asked, r.Form.Get("ExternalId")+"|"+r.Form.Get("RoleSessionName"))
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, recordAssumeRole)
	}))
	t.Cleanup(sts.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", sts.URL)
	const role = "arn:aws:iam::111111111111:role/a"
	for _, settings := range []serviceSettings{
		{region: "eu-west-1", roleARN: role},
		{region: "eu-west-1", roleARN: role, externalID: "ext-1", sessionName: "ci-42"},
		{region: "eu-west-2", roleARN: role, externalID: "ext-1", sessionName: "ci-42"},
	} {
		s := &Service{}
		s.Begin(ctx, harvest.Settings{})
		s.SetSettings(settings)
		if _, err := s.sdkConfig(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"|infraharvest", "ext-1|ci-42"}
	if !slices.Equal(asked, want) {
		t.Errorf("AssumeRole asked with %q, want %q (the second region shares the credentials)", asked, want)
	}
}
