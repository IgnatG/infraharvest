package providerwrapper //nolint

import (
	"errors"
	"strings"
	"testing"
	"time"

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

// countingProvider fails every read with readError and counts the reads.
type countingProvider struct {
	failingProvider
	readError string
	reads     int
}

func (p *countingProvider) ReadResource(providers.ReadResourceRequest) providers.ReadResourceResponse {
	p.reads++
	var diags tfdiags.Diagnostics
	return providers.ReadResourceResponse{Diagnostics: diags.Append(errors.New(p.readError))}
}

func refreshWith(provider *countingProvider, retryCount int) error {
	p := &ProviderWrapper{Provider: provider, retryCount: retryCount, retrySleepMs: 1}
	_, err := p.Refresh(
		&terraform.InstanceInfo{Type: "fake_thing", Id: "fake_thing.a"},
		&terraform.InstanceState{ID: "a", Attributes: map[string]string{"id": "a"}},
	)
	return err
}

func TestRefreshRetriesThrottledReads(t *testing.T) {
	provider := &countingProvider{readError: "read: throttled"}

	if err := refreshWith(provider, 3); err == nil {
		t.Fatal("want an error")
	}

	if provider.reads != 3 {
		t.Errorf("reads: got %d, want 3", provider.reads)
	}
}

func TestRefreshDoesNotRetryMissingResources(t *testing.T) {
	provider := &countingProvider{readError: "read: resource not found"}

	if err := refreshWith(provider, 5); err == nil {
		t.Fatal("want an error")
	}

	if provider.reads != 1 {
		t.Errorf("reads: got %d, want 1", provider.reads)
	}
}

// With no read attempts there is no read error to report; the message
// must not wrap a nil error.
func TestRefreshWithoutReadsReportsImportError(t *testing.T) {
	err := refreshWith(&countingProvider{readError: "unused"}, 0)

	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "import: not importable") || strings.Contains(err.Error(), "%!w") {
		t.Errorf("unexpected error %q", err)
	}
}

func TestRetryDelay(t *testing.T) {
	for attempt, want := range []struct{ low, high time.Duration }{
		{150 * time.Millisecond, 300 * time.Millisecond},
		{300 * time.Millisecond, 600 * time.Millisecond},
		{600 * time.Millisecond, 1200 * time.Millisecond},
	} {
		for range 20 {
			if got := retryDelay(attempt, 300); got < want.low || got > want.high {
				t.Errorf("attempt %d: got %s, want between %s and %s", attempt, got, want.low, want.high)
			}
		}
	}
	if got := retryDelay(20, 300); got > maxRetryDelay {
		t.Errorf("attempt 20: got %s, want at most %s", got, maxRetryDelay)
	}
	if got := retryDelay(3, 0); got != 0 {
		t.Errorf("base 0: got %s, want 0", got)
	}
}
