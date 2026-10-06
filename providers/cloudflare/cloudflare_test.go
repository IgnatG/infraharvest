// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
)

// listed is the part of a resource a lister decides.
type listed struct {
	ID, Name, Type string
	Attributes     map[string]string
}

func summarize(resources []terraformutils.Resource) []listed {
	var out []listed
	for _, r := range resources {
		attrs := map[string]string{}
		for k, v := range r.InstanceState.Attributes {
			attrs[k] = v
		}
		out = append(out, listed{r.InstanceState.ID, r.RawName, r.InstanceInfo.Type, attrs})
	}
	return out
}

// fakeCloudflare serves the Cloudflare API paths the listers call. Each
// list is served one item per page, so every lister has to page through.
type fakeCloudflare struct {
	t *testing.T
	// lists maps a path to the items it lists.
	lists map[string][]interface{}
	// gone are the paths that answer 410 Gone, as retired APIs do.
	gone map[string]bool
	// missing are the paths that answer 404 Not Found.
	missing map[string]bool
	// unpaged maps a path to the result of an API that isn't paginated.
	unpaged map[string][]interface{}
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer token" {
		f.t.Errorf("%s: Authorization = %q, want the API token", r.URL.Path, got)
	}
	for _, h := range []string{"X-Auth-Key", "X-Auth-Email"} {
		if got := r.Header.Get(h); got != "" {
			f.t.Errorf("%s: %s = %q, want no API key credentials with a token", r.URL.Path, h, got)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if f.gone[r.URL.Path] {
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"gone"}],"messages":[]}`)
		return
	}
	if result, ok := f.unpaged[r.URL.Path]; ok {
		writeJSON(f.t, w, map[string]interface{}{"success": true, "errors": []interface{}{}, "messages": []interface{}{}, "result": result})
		return
	}
	list, ok := f.lists[r.URL.Path]
	if !ok && !f.missing[r.URL.Path] {
		f.t.Errorf("unexpected request %s", r.URL)
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":7003,"message":"not found"}],"messages":[]}`)
		return
	}
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		var err error
		if page, err = strconv.Atoi(p); err != nil {
			f.t.Errorf("%s: page %q", r.URL.Path, p)
		}
	}
	result := []interface{}{}
	if page >= 1 && page <= len(list) {
		result = append(result, list[page-1])
	}
	writeJSON(f.t, w, map[string]interface{}{
		"success":     true,
		"errors":      []interface{}{},
		"messages":    []interface{}{},
		"result":      result,
		"result_info": map[string]int{"page": page, "per_page": 1, "count": len(result), "total_count": len(list), "total_pages": len(list)},
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding response: %v", err)
	}
}

func items(objects ...string) []interface{} {
	var out []interface{}
	for _, o := range objects {
		var v interface{}
		if err := json.Unmarshal([]byte(o), &v); err != nil {
			panic(err)
		}
		out = append(out, v)
	}
	return out
}

// serve points the listers at f, authenticated with an API token. The API
// key variables are set too, to check the token wins.
func serve(t *testing.T, f *fakeCloudflare) {
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_BASE_URL", srv.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("CLOUDFLARE_API_KEY", "key")
	t.Setenv("CLOUDFLARE_EMAIL", "user@example.com")
	t.Setenv("CLOUDFLARE_API_USER_SERVICE_KEY", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acc1")
}

var twoZones = items(`{"id":"z1","name":"example.com"}`, `{"id":"z2","name":"example.org"}`)

func TestDNSInitResources(t *testing.T) {
	serve(t, &fakeCloudflare{lists: map[string][]interface{}{
		"/zones": twoZones,
		"/zones/z1/dns_records": items(
			`{"id":"r1","name":"www.example.com","type":"A","content":"192.0.2.1"}`,
			`{"id":"r2","name":"example.com","type":"MX","content":"mx.example.com"}`),
		"/zones/z2/dns_records": items(),
	}})
	g := &DNSGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"z1", "example.com", "cloudflare_zone", map[string]string{"id": "z1"}},
		{"z1/r1", "A_example.com_r1", "cloudflare_dns_record", map[string]string{"zone_id": "z1", "domain": "example.com", "name": "www.example.com"}},
		{"z1/r2", "MX_example.com_r2", "cloudflare_dns_record", map[string]string{"zone_id": "z1", "domain": "example.com", "name": "example.com"}},
		{"z2", "example.org", "cloudflare_zone", map[string]string{"id": "z2"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestAccessInitResources(t *testing.T) {
	serve(t, &fakeCloudflare{lists: map[string][]interface{}{
		"/accounts/acc1/access/apps": items(`{"id":"app0","name":"Portal","type":"self_hosted"}`, `{"id":"app9","name":"Launcher","type":"saas"}`),
		"/zones":                     twoZones,
		"/zones/z1/access/apps":      items(`{"id":"app1","name":"Wiki","type":"self_hosted"}`, `{"id":"app2","name":"Mail","type":"self_hosted"}`),
		"/zones/z2/access/apps":      items(),
	}})
	g := &AccessGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"accounts/acc1/app0", "Portal_app0", "cloudflare_zero_trust_access_application", map[string]string{"account_id": "acc1", "name": "Portal"}},
		{"accounts/acc1/app9", "Launcher_app9", "cloudflare_zero_trust_access_application", map[string]string{"account_id": "acc1", "name": "Launcher"}},
		{"zones/z1/app1", "Wiki_app1", "cloudflare_zero_trust_access_application", map[string]string{"zone_id": "z1", "name": "Wiki"}},
		{"zones/z1/app2", "Mail_app2", "cloudflare_zero_trust_access_application", map[string]string{"zone_id": "z1", "name": "Mail"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

// TestAccessWithoutAccountID checks that without CLOUDFLARE_ACCOUNT_ID only
// the zones' applications are listed: the fake fails any account request.
func TestAccessWithoutAccountID(t *testing.T) {
	serve(t, &fakeCloudflare{lists: map[string][]interface{}{
		"/zones":                twoZones,
		"/zones/z1/access/apps": items(`{"id":"app1","name":"Wiki","type":"self_hosted"}`),
		"/zones/z2/access/apps": items(),
	}})
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	g := &AccessGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"zones/z1/app1", "Wiki_app1", "cloudflare_zero_trust_access_application", map[string]string{"zone_id": "z1", "name": "Wiki"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestAccountMemberInitResources(t *testing.T) {
	serve(t, &fakeCloudflare{lists: map[string][]interface{}{
		"/accounts/acc1/members": items(`{"id":"m1","user":{"email":"a@example.com"}}`, `{"id":"m2","user":{"email":"b@example.com"}}`),
	}})
	g := &AccountMemberGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"acc1/m1", "m1", "cloudflare_account_member", map[string]string{"email_address": "a@example.com"}},
		{"acc1/m2", "m2", "cloudflare_account_member", map[string]string{"email_address": "b@example.com"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestAccountMemberNeedsAccountID(t *testing.T) {
	serve(t, &fakeCloudflare{})
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	g := &AccountMemberGenerator{}
	if err := g.InitResources(); err == nil {
		t.Error("InitResources without CLOUDFLARE_ACCOUNT_ID succeeded, want an error")
	}
}

func TestPageRuleInitResources(t *testing.T) {
	f := &fakeCloudflare{
		lists: map[string][]interface{}{"/zones": twoZones},
		unpaged: map[string][]interface{}{
			"/zones/z1/pagerules": items(`{"id":"p1","priority":1,"status":"active"}`, `{"id":"p2","priority":2,"status":"disabled"}`),
			"/zones/z2/pagerules": items(),
		},
	}
	serve(t, f)
	g := &PageRulesGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"z1/p1", "p1", "cloudflare_page_rule", map[string]string{"zone_id": "z1"}},
		{"z1/p2", "p2", "cloudflare_page_rule", map[string]string{"zone_id": "z1"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestFirewallInitResources(t *testing.T) {
	serve(t, &fakeCloudflare{
		lists: map[string][]interface{}{
			"/zones": items(`{"id":"z1","name":"example.com"}`),
			"/accounts/acc1/firewall/access_rules/rules": items(`{"id":"ar1","mode":"block","scope":{"type":"organization"}}`),
			"/zones/z1/firewall/rules":                   items(`{"id":"fr1","action":"block"}`, `{"id":"fr2","action":"allow"}`),
			"/zones/z1/filters":                          items(`{"id":"f1","expression":"ip.src eq 192.0.2.1"}`),
			"/zones/z1/firewall/access_rules/rules": items(
				`{"id":"ar1","mode":"block","scope":{"type":"organization"}}`,
				`{"id":"zr1","mode":"challenge","scope":{"type":"zone"}}`,
				`{"id":"ur1","mode":"whitelist","scope":{"type":"user"}}`),
			"/zones/z1/firewall/lockdowns": items(`{"id":"l1","urls":["example.com/admin"]}`, `{"id":"l2","urls":["example.com/login"]}`),
			"/zones/z1/rate_limits":        items(`{"id":"rl1"}`, `{"id":"rl2"}`),
		},
	})
	g := &FirewallGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"accounts/acc1/ar1", "ar1", "cloudflare_access_rule", map[string]string{}},
		{"z1/fr1", "example.com_fr1", "cloudflare_firewall_rule", map[string]string{"zone_id": "z1"}},
		{"z1/fr2", "example.com_fr2", "cloudflare_firewall_rule", map[string]string{"zone_id": "z1"}},
		{"z1/f1", "example.com_f1", "cloudflare_filter", map[string]string{"zone_id": "z1"}},
		{"zones/z1/zr1", "example.com_zr1", "cloudflare_access_rule", map[string]string{"zone_id": "z1"}},
		{"z1/l1", "example.com_l1", "cloudflare_zone_lockdown", map[string]string{"zone_id": "z1", "zone": "example.com"}},
		{"z1/l2", "example.com_l2", "cloudflare_zone_lockdown", map[string]string{"zone_id": "z1", "zone": "example.com"}},
		{"z1/rl1", "z1_rl1", "cloudflare_rate_limit", map[string]string{}},
		{"z1/rl2", "z1_rl2", "cloudflare_rate_limit", map[string]string{}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

// TestFirewallSkipsRetiredAPIs checks that the APIs Cloudflare has retired,
// which answer 410 Gone, list nothing instead of failing the service.
func TestFirewallSkipsRetiredAPIs(t *testing.T) {
	serve(t, &fakeCloudflare{
		lists: map[string][]interface{}{
			"/zones": items(`{"id":"z1","name":"example.com"}`),
			"/accounts/acc1/firewall/access_rules/rules": items(),
			"/zones/z1/firewall/access_rules/rules":      items(),
			"/zones/z1/firewall/lockdowns":               items(`{"id":"l1","urls":["example.com/admin"]}`),
		},
		gone: map[string]bool{
			"/zones/z1/firewall/rules": true,
			"/zones/z1/filters":        true,
			"/zones/z1/rate_limits":    true,
		},
	})
	g := &FirewallGenerator{}
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	want := []listed{
		{"z1/l1", "example.com_l1", "cloudflare_zone_lockdown", map[string]string{"zone_id": "z1", "zone": "example.com"}},
	}
	if got := summarize(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestFirewallFailsOnOtherErrors(t *testing.T) {
	serve(t, &fakeCloudflare{
		lists: map[string][]interface{}{
			"/zones": items(`{"id":"z1","name":"example.com"}`),
			"/accounts/acc1/firewall/access_rules/rules": items(),
		},
		// Any error other than 410 Gone fails the service.
		missing: map[string]bool{"/zones/z1/firewall/rules": true},
	})
	g := &FirewallGenerator{}
	if err := g.InitResources(); err == nil {
		t.Error("InitResources succeeded, want the 404 as an error")
	}
}

func TestAuthOptions(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	for _, tc := range []struct {
		name    string
		vars    map[string]string
		want    http.Header
		wantErr bool
	}{
		{
			name: "token",
			vars: map[string]string{"CLOUDFLARE_API_TOKEN": "t", "CLOUDFLARE_API_KEY": "k", "CLOUDFLARE_EMAIL": "e"},
			want: http.Header{"Authorization": {"Bearer t"}},
		},
		{
			name: "key and email",
			vars: map[string]string{"CLOUDFLARE_API_KEY": "k", "CLOUDFLARE_EMAIL": "e"},
			want: http.Header{"X-Auth-Key": {"k"}, "X-Auth-Email": {"e"}},
		},
		{name: "key without email", vars: map[string]string{"CLOUDFLARE_API_KEY": "k"}, wantErr: true},
		{name: "nothing", vars: map[string]string{}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan http.Header, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h := http.Header{}
				for _, k := range []string{"Authorization", "X-Auth-Key", "X-Auth-Email", "X-Auth-User-Service-Key"} {
					if v := r.Header.Get(k); v != "" {
						h.Set(k, v)
					}
				}
				got <- h
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[],"result_info":{"page":1,"per_page":1,"total_pages":0}}`)
			}))
			defer srv.Close()
			// The SDK client adds every credential it finds in the
			// environment: only the chosen scheme may reach the API.
			t.Setenv("CLOUDFLARE_BASE_URL", srv.URL)
			t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
			t.Setenv("CLOUDFLARE_API_KEY", "env-key")
			t.Setenv("CLOUDFLARE_EMAIL", "env-email")
			t.Setenv("CLOUDFLARE_API_USER_SERVICE_KEY", "env-service-key")

			opts, err := authOptions(env(tc.vars))
			if tc.wantErr {
				if err == nil {
					t.Fatal("authOptions succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c := cloudflare.NewClient(opts...)
			if _, err := listZones(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if h := <-got; !reflect.DeepEqual(h, tc.want) {
				t.Errorf("headers = %v, want %v", h, tc.want)
			}
		})
	}
}
