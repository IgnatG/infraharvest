package providerwrapper //nolint

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/providers"
	"github.com/hashicorp/terraform/terraform"
	"github.com/hashicorp/terraform/tfdiags"
	"github.com/zclconf/go-cty/cty"
)

// failingProvider fails both ReadResource and ImportResourceState.
type failingProvider struct {
	providers.Interface
}

func (failingProvider) GetSchema() providers.GetSchemaResponse {
	return providers.GetSchemaResponse{ResourceTypes: map[string]providers.Schema{
		"fake_thing": {Block: &configschema.Block{
			Attributes: map[string]*configschema.Attribute{"id": {Type: cty.String, Computed: true}},
		}},
	}}
}

func (failingProvider) ReadResource(providers.ReadResourceRequest) providers.ReadResourceResponse {
	var diags tfdiags.Diagnostics
	return providers.ReadResourceResponse{Diagnostics: diags.Append(errors.New("read: throttled"))}
}

func (failingProvider) ImportResourceState(providers.ImportResourceStateRequest) providers.ImportResourceStateResponse {
	var diags tfdiags.Diagnostics
	return providers.ImportResourceStateResponse{Diagnostics: diags.Append(errors.New("import: not importable"))}
}

func TestRefreshReportsReadAndImportErrors(t *testing.T) {
	p := &ProviderWrapper{Provider: failingProvider{}, retryCount: 1}

	_, err := p.Refresh(
		&terraform.InstanceInfo{Type: "fake_thing", Id: "fake_thing.a"},
		&terraform.InstanceState{ID: "a", Attributes: map[string]string{"id": "a"}},
	)

	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"read: throttled", "import: not importable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
