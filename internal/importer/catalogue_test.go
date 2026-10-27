// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
)

// widgetCatalogue is the built-in AWS catalogue with an entry that sets
// every field the engine's options take, some only for provider 6.x, and
// one that sets none.
func widgetCatalogue(t *testing.T) *catalogue.Catalogue {
	t.Helper()
	dir := t.TempDir()
	content := `provider: aws
service: widgets
types:
  aws_widget:
    omit: [legacy, "rules when @kind=managed"]
    defaults: [size=1]
    state_only: [force_destroy]
    keep_empty: [tags]
    provider_versions:
      ">= 6.0":
        omit: [renamed]
        defaults: [mode=fast]
        state_only: [skip_destroy]
    data_source: {type: aws_widget, argument: name}
    arn: {service: widgets, resource_type: widget}
    placeholder:
      one_of: [code, image]
      set: {code: "widgets/{name}.zip"}
      ignore: [code]
      reason: the code is deployed outside Terraform
    lifecycle: {create_before_destroy: true, reason: others refer to it}
  aws_gadget: {}
`
	if err := os.WriteFile(filepath.Join(dir, "widget.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("aws", dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// Each of the catalogue's fields ends up in the engine's option of the
// same meaning, with what the provider release adds, and a type that sets
// none of them gets none.
func TestCatalogueEngineOptions(t *testing.T) {
	cat := widgetCatalogue(t)
	v5 := goversion.Must(goversion.NewVersion("5.100.0"))
	v6 := goversion.Must(goversion.NewVersion("6.2.0"))
	opts, older := catalogueEngineOptions(cat, v6), catalogueEngineOptions(cat, v5)
	_, gadgetDataSource := opts.DataSources["aws_gadget"]
	_, gadgetARN := opts.ARNs["aws_gadget"]
	_, gadgetPlaceholder := opts.Placeholders["aws_gadget"]
	_, gadgetLifecycle := opts.Lifecycles["aws_gadget"]
	for _, tt := range []struct {
		name      string
		got, want any
	}{
		{"omit, without lister conditions", opts.Omit["aws_widget"], []string{"legacy", "renamed"}},
		{"omit for an older release", older.Omit["aws_widget"], []string{"legacy"}},
		{"omit for every type", opts.Omit[catalogue.AllTypes], cat.Omit(v6)[catalogue.AllTypes]},
		{"defaults", opts.Defaults["aws_widget"], []string{"size=1", "mode=fast"}},
		{"defaults for an older release", older.Defaults["aws_widget"], []string{"size=1"}},
		{"state_only", opts.StateOnly["aws_widget"], []string{"force_destroy", "skip_destroy"}},
		{"state_only for an older release", older.StateOnly["aws_widget"], []string{"force_destroy"}},
		{"keep_empty", opts.KeepEmpty["aws_widget"], []string{"tags"}},
		{"data source", opts.DataSources["aws_widget"], engine.DataSource{Type: "aws_widget", Argument: "name"}},
		{"ARN", opts.ARNs["aws_widget"], engine.ARN{Service: "widgets", ResourceType: "widget"}},
		{"placeholder", opts.Placeholders["aws_widget"], engine.Placeholder{OneOf: []string{"code", "image"}, Set: map[string]string{"code": "widgets/{name}.zip"}, Ignore: []string{"code"}, Reason: "the code is deployed outside Terraform"}},
		{"lifecycle", opts.Lifecycles["aws_widget"], engine.Lifecycle{CreateBeforeDestroy: true, Reason: "others refer to it"}},
		{"schema metadata", opts.SchemaMetadata != nil, true},
		{"no omit for a type without", opts.Omit["aws_gadget"], []string(nil)},
		{"no data source for a type without", gadgetDataSource, false},
		{"no ARN for a type without", gadgetARN, false},
		{"no placeholder for a type without", gadgetPlaceholder, false},
		{"no lifecycle for a type without", gadgetLifecycle, false},
	} {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s: got %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}

// The verification gate and drift take the catalogue's options for the
// release the root pins, and nothing else of it.
func TestCatalogueVerifyAndDriftOptions(t *testing.T) {
	cat := widgetCatalogue(t)
	v6 := goversion.Must(goversion.NewVersion("6.2.0"))
	verify, drift := catalogueVerifyOptions(cat, v6), catalogueDriftOptions(cat, v6)
	for _, tt := range []struct {
		name      string
		got, want any
	}{
		{"verify: omit", verify.Omit, cat.Omit(v6)},
		{"verify: state_only", verify.StateOnly, cat.StateOnly(v6)},
		{"verify: the release's omit", verify.Omit["aws_widget"], []string{"legacy", "renamed"}},
		{"verify: nothing else", engine.Options{Omit: verify.Omit, StateOnly: verify.StateOnly}, verify},
		{"drift: omit", drift.Omit, cat.Omit(v6)},
		{"drift: defaults", drift.Defaults, cat.Defaults(v6)},
		{"drift: state_only", drift.StateOnly, cat.StateOnly(v6)},
		{"drift: the release's state_only", drift.StateOnly["aws_widget"], []string{"force_destroy", "skip_destroy"}},
		{"drift: nothing else", engine.DriftOptions{Omit: drift.Omit, Defaults: drift.Defaults, StateOnly: drift.StateOnly}, drift},
	} {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s: got %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}
