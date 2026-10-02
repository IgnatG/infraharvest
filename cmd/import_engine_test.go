// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestImportsByDir(t *testing.T) {
	resources := map[string][]terraformutils.Resource{
		"sqs": {terraformutils.NewSimpleResource("https://sqs/1/orders", "orders", "aws_sqs_queue", "aws", nil)},
		"sns": {terraformutils.NewSimpleResource("arn:aws:sns:topic", "alerts", "aws_sns_topic", "aws", nil)},
		"s3":  nil, // a service with no resources gets no directory
	}

	t.Run("one directory per service", func(t *testing.T) {
		options := ImportOptions{PathPattern: DefaultPathPattern, PathOutput: "out"}

		got := importsByDir("aws", options, resources)

		want := map[string][]engine.Import{
			filepath.Join("out", "aws", "sqs"): {{Type: "aws_sqs_queue", Name: "tfer--orders", ID: "https://sqs/1/orders"}},
			filepath.Join("out", "aws", "sns"): {{Type: "aws_sns_topic", Name: "tfer--alerts", ID: "arn:aws:sns:topic"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("one directory for a pattern without {service}", func(t *testing.T) {
		options := ImportOptions{PathPattern: "{output}/{provider}/", PathOutput: "out"}

		got := importsByDir("aws", options, resources)

		if len(got) != 1 || len(got[filepath.Join("out", "aws")]) != 2 {
			t.Errorf("want both resources in out/aws, got %v", got)
		}
	})
}

type sourcedProvider struct {
	terraformutils.ProviderGenerator
	data map[string]interface{}
}

func (p sourcedProvider) GetName() string   { return "datadog" }
func (p sourcedProvider) GetSource() string { return "DataDog/datadog" }
func (p sourcedProvider) GetProviderData(...string) map[string]interface{} {
	return p.data
}

func TestEngineProvider(t *testing.T) {
	p := sourcedProvider{data: map[string]interface{}{
		"provider": map[string]interface{}{"datadog": map[string]interface{}{"api_url": "https://api.datadoghq.eu"}},
	}}

	got := engineProvider(p)

	want := engine.Provider{Name: "datadog", Source: "DataDog/datadog", Config: map[string]interface{}{"api_url": "https://api.datadoghq.eu"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Unexpected provider data must not panic.
	if got := engineProvider(sourcedProvider{data: map[string]interface{}{"provider": "oops"}}); got.Config != nil {
		t.Errorf("want no config from malformed data, got %v", got.Config)
	}
}

func TestCheckTerraformEngineOptions(t *testing.T) {
	defaults := ImportOptions{State: DefaultState, Output: "hcl", Connect: true}
	if err := checkTerraformEngineOptions(defaults); err != nil {
		t.Errorf("defaults must be accepted, got %v", err)
	}

	bad := defaults
	bad.State, bad.Output, bad.Compact = "bucket", "json", true
	err := checkTerraformEngineOptions(bad)
	if err == nil {
		t.Fatal("want an error for unsupported options")
	}
	for _, want := range []string{"--state bucket", "--output json", "--compact"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestImportRejectsUnknownEngine(t *testing.T) {
	err := Import(&fakeProvider{}, ImportOptions{Engine: "tofu-ish"}, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown --engine "tofu-ish"`) {
		t.Errorf("want an unknown engine error, got %v", err)
	}
}
