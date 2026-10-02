package terraformutils

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/providers"
	"github.com/hashicorp/terraform/terraform"
	"github.com/zclconf/go-cty/cty"
)

// fakeRefresher returns live state for IDs starting with "ok", no state for
// "gone" and an error for everything else.
type fakeRefresher struct{}

func (fakeRefresher) Refresh(_ *terraform.InstanceInfo, state *terraform.InstanceState) (*terraform.InstanceState, error) {
	switch {
	case strings.HasPrefix(state.ID, "ok"):
		return &terraform.InstanceState{ID: state.ID, Attributes: map[string]string{"id": state.ID}}, nil
	case state.ID == "gone":
		return &terraform.InstanceState{}, nil
	default:
		return nil, errors.New("access denied")
	}
}

func TestRefreshResourcesReportsEveryDroppedResource(t *testing.T) {
	newRes := func(id string) *Resource {
		r := NewSimpleResource(id, id, "fake_thing", "fake", nil)
		return &r
	}
	regular := []*Resource{newRes("ok-1"), newRes("denied"), newRes("gone")}
	slow := [][]*Resource{{newRes("ok-2"), newRes("denied-slow")}}

	refreshed, failures := RefreshResources(regular, fakeRefresher{}, slow)

	var ids []string
	for _, r := range refreshed {
		ids = append(ids, r.InstanceState.ID)
	}
	if strings.Join(ids, ",") != "ok-1,ok-2" {
		t.Errorf("refreshed: got %v, want [ok-1 ok-2]", ids)
	}

	want := []string{
		"refresh fake_thing.tfer--denied-slow: access denied",
		"refresh fake_thing.tfer--denied: access denied",
		"refresh fake_thing.tfer--gone: provider returned no state",
	}
	if len(failures) != len(want) {
		t.Fatalf("failures: got %v, want %d entries", failures, len(want))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(failures[i].Error(), prefix) {
			t.Errorf("failure %d: got %q, want prefix %q", i, failures[i], prefix)
		}
	}
}

type fakeSchema struct{ types []string }

func (f fakeSchema) GetSchema() *providers.GetSchemaResponse {
	resp := &providers.GetSchemaResponse{ResourceTypes: map[string]providers.Schema{}}
	for _, t := range f.types {
		resp.ResourceTypes[t] = providers.Schema{Block: &configschema.Block{
			Attributes: map[string]*configschema.Attribute{"name": {Type: cty.String, Optional: true}},
		}}
	}
	return resp
}

func TestConvertTFStatesDropsAndReportsFailures(t *testing.T) {
	provider := &stubProvider{service: &Service{}}
	known := NewResource("a", "a", "fake_known", "fake", map[string]string{"name": "a"}, nil, nil)
	unknown := NewSimpleResource("b", "b", "fake_unknown", "fake", nil)
	mapping := NewProvidersMapping(provider)
	mapping.Providers[provider] = true
	for _, r := range []*Resource{&known, &unknown} {
		mapping.Resources[r] = true
		mapping.resourceToProvider[r] = provider
	}

	failures := mapping.ConvertTFStates(fakeSchema{types: []string{"fake_known"}})

	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "convert fake_unknown.tfer--b: provider schema has no resource type fake_unknown") {
		t.Errorf("want one conversion failure for fake_unknown.tfer--b, got %v", failures)
	}
	got := provider.GetService().GetResources()
	if len(got) != 1 || got[0].InstanceInfo.Id != "fake_known.tfer--a" {
		t.Errorf("want only fake_known.a kept, got %v", got)
	}
}

type failingHookService struct{ Service }

func (failingHookService) PostConvertHook() error { return errors.New("bad policy JSON") }

func TestCleanupProvidersReportsHookFailures(t *testing.T) {
	provider := &stubProvider{service: &failingHookService{}}
	mapping := NewProvidersMapping(provider)
	mapping.Providers[provider] = true
	mapping.providerToService[provider] = "sqs"

	failures := mapping.CleanupProviders()

	if len(failures) != 1 || failures[0].Error() != "post-convert hook for service sqs: bad policy JSON" {
		t.Errorf("want one hook failure for sqs, got %v", failures)
	}
}

type stubProvider struct {
	ProviderGenerator
	service ServiceGenerator
}

func (p *stubProvider) GetService() ServiceGenerator { return p.service }
