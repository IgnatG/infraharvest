// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/dns/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// cloudDNS serves Cloud DNS's zone and record set lists, each on two
// pages: a public zone, example-com, and a private one, internal.
func cloudDNS(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, r.Method+" "+r.URL.Path, http.StatusMethodNotAllowed)
			return
		}
		page := r.URL.Query().Get("pageToken")
		switch {
		case r.URL.Path == "/dns/v1/projects/acme/managedZones" && page == "":
			_, _ = w.Write([]byte(`{"managedZones": [{"name": "example-com", "dnsName": "example.com.", "visibility": "public"}], "nextPageToken": "z2"}`))
		case r.URL.Path == "/dns/v1/projects/acme/managedZones" && page == "z2":
			_, _ = w.Write([]byte(`{"managedZones": [{"name": "internal", "dnsName": "internal.acme.", "visibility": "private"}]}`))
		case r.URL.Path == "/dns/v1/projects/acme/managedZones/example-com/rrsets" && page == "":
			_, _ = w.Write([]byte(`{"rrsets": [
				{"name": "example.com.", "type": "NS", "ttl": 21600, "rrdatas": ["ns-cloud-a1.googledomains.com."]},
				{"name": "example.com.", "type": "SOA", "ttl": 21600, "rrdatas": ["ns-cloud-a1.googledomains.com. cloud-dns-hostmaster.google.com. 1 21600 3600 259200 300"]}
			], "nextPageToken": "r2"}`))
		case r.URL.Path == "/dns/v1/projects/acme/managedZones/example-com/rrsets" && page == "r2":
			_, _ = w.Write([]byte(`{"rrsets": [
				{"name": "www.example.com.", "type": "A", "ttl": 300, "rrdatas": ["203.0.113.10"]},
				{"name": "dev.example.com.", "type": "NS", "ttl": 300, "rrdatas": ["ns1.dev.example.net."]}
			]}`))
		case r.URL.Path == "/dns/v1/projects/acme/managedZones/internal/rrsets":
			_, _ = w.Write([]byte(`{"rrsets": [{"name": "db.internal.acme.", "type": "CNAME", "ttl": 60, "rrdatas": ["orders.internal.acme."]}]}`))
		default:
			http.Error(w, `{"error": {"code": 404, "message": "`+r.URL.Path+`"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newDNSRecordSets(t *testing.T, region string) *DNSRecordSetsLister {
	t.Helper()
	g := &DNSRecordSetsLister{}
	g.Configure(harvest.ListerConfig{Provider: "google", Settings: map[string]interface{}{"project": "acme", "region": compute.Region{Name: region}}})
	g.SetContext(t.Context())
	return g
}

// Every record set of every zone, from every page, imported by the
// provider's full ID; the SOA and the zone's own NS set, which Cloud DNS
// creates with the zone, are left out by default.
func TestDNSRecordSetsList(t *testing.T) {
	t.Setenv(EndpointEnv, cloudDNS(t).URL)
	g := newDNSRecordSets(t, "")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range g.Resources {
		got = append(got, r.Address.Type+" "+r.Observed.ID)
	}
	sort.Strings(got)
	want := []string{
		"google_dns_record_set projects/acme/managedZones/example-com/rrsets/dev.example.com./NS",
		"google_dns_record_set projects/acme/managedZones/example-com/rrsets/example.com./NS",
		"google_dns_record_set projects/acme/managedZones/example-com/rrsets/example.com./SOA",
		"google_dns_record_set projects/acme/managedZones/example-com/rrsets/www.example.com./A",
		"google_dns_record_set projects/acme/managedZones/internal/rrsets/db.internal.acme./CNAME",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	cat := catalogue.MustBuiltin("google")
	skipped := map[string]bool{}
	for _, r := range g.Resources {
		a := r.Observed.Attributes
		if a["project"] != "acme" || a["managed_zone"] == "" || a["name"] == "" || a["type"] == "" || a["dns_name"] == "" {
			t.Errorf("%s: attributes %v", r.Observed.ID, a)
		}
		if cat.Skipped(r.Address.Type, r.Observed.ID, a) != "" {
			skipped[r.Observed.ID] = true
		}
		if r.Observed.ID == "projects/acme/managedZones/example-com/rrsets/www.example.com./A" && r.RawName != "example-com-www-A" {
			t.Errorf("name %q", r.RawName)
		}
	}
	if len(skipped) != 2 || !skipped["projects/acme/managedZones/example-com/rrsets/example.com./SOA"] || !skipped["projects/acme/managedZones/example-com/rrsets/example.com./NS"] {
		t.Errorf("left out by default: %v, want the zone's own SOA and NS sets", skipped)
	}
}

// A record set is named after its zone, its name within the zone and its
// type: before, the zone's DNS name was in the name twice, once as the
// zone's name and again at the end of the set's.
func TestRecordSetName(t *testing.T) {
	zone := &dns.ManagedZone{Name: "shop", DnsName: "shop.example.com."}
	for _, tt := range []struct{ name, typ, want string }{
		{"app.shop.example.com.", "A", "shop-app-A"},
		{"a.b.Shop.Example.com.", "CNAME", "shop-a.b-CNAME"},
		{"shop.example.com.", "MX", "shop-MX"},
		{"other.example.net.", "A", "shop-other.example.net-A"},
		{"myshop.example.com.", "A", "shop-myshop.example.com-A"},
	} {
		if got := recordSetName(zone, &dns.ResourceRecordSet{Name: tt.name, Type: tt.typ}); got != tt.want {
			t.Errorf("recordSetName(%q, %q): got %q, want %q", tt.name, tt.typ, got, tt.want)
		}
	}
}

// Zones are global: a region's import lists no record sets.
func TestDNSRecordSetsInARegion(t *testing.T) {
	t.Setenv(EndpointEnv, cloudDNS(t).URL)
	g := newDNSRecordSets(t, "us-central1")
	if err := g.List(); err != nil || len(g.Resources) != 0 {
		t.Errorf("got %v, %v", g.Resources, err)
	}
}

// Without the Cloud DNS API there are no zones, which isn't a failure; any
// other failure is.
func TestDNSRecordSetsErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{
			name:   "API disabled",
			status: http.StatusForbidden,
			body:   `{"error": {"code": 403, "status": "PERMISSION_DENIED", "message": "Cloud DNS API has not been used in project acme before or it is disabled.", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "SERVICE_DISABLED", "domain": "googleapis.com"}]}}`,
		},
		{
			name:    "permission denied",
			status:  http.StatusForbidden,
			body:    `{"error": {"code": 403, "status": "PERMISSION_DENIED", "message": "Forbidden"}}`,
			wantErr: true,
		},
		{
			name:    "server error",
			status:  http.StatusInternalServerError,
			body:    `{"error": {"code": 500, "message": "boom"}}`,
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			t.Setenv(EndpointEnv, server.URL)
			g := newDNSRecordSets(t, "")
			err := g.List()
			if (err != nil) != tt.wantErr {
				t.Errorf("error %v, want one: %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "acme") {
				t.Errorf("error %q doesn't name the project", err)
			}
			if len(g.Resources) != 0 {
				t.Errorf("%d resources, want none", len(g.Resources))
			}
		})
	}
}

// A zone whose record sets can't be listed fails the listing, rather than
// leaving its records out unnoticed.
func TestDNSRecordSetsZoneFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/dns/v1/projects/acme/managedZones" {
			_, _ = w.Write([]byte(`{"managedZones": [{"name": "example-com", "dnsName": "example.com."}]}`))
			return
		}
		http.Error(w, `{"error": {"code": 500, "message": "boom"}}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)
	g := newDNSRecordSets(t, "")
	if err := g.List(); err == nil || !strings.Contains(err.Error(), "example-com") {
		t.Errorf("error %v, want one naming the zone", err)
	}
}
