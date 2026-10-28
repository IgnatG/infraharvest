// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfquery

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// fakeTerraform plays back terraform query -json output recorded from
// Terraform 1.16 (testdata/query.jsonl) and checks the working directory
// List wrote.
type fakeTerraform struct {
	t       *testing.T
	dir     string
	schemas *tfjson.ProviderSchemas
	output  []byte
	exit    error
	// queried is the list blocks the working directory had when it was
	// queried.
	queried string
}

func (f *fakeTerraform) Init(context.Context, ...tfexec.InitOption) error { return nil }

func (f *fakeTerraform) ProvidersSchema(context.Context) (*tfjson.ProviderSchemas, error) {
	return f.schemas, nil
}

func (f *fakeTerraform) QueryJSON(context.Context, ...tfexec.QueryOption) (iter.Seq[tfexec.NextMessage], error) {
	content, err := os.ReadFile(filepath.Join(f.dir, FileName))
	if err != nil {
		f.t.Fatal(err)
	}
	f.queried = string(content)
	return func(yield func(tfexec.NextMessage) bool) {
		scanner := bufio.NewScanner(bytes.NewReader(f.output))
		for scanner.Scan() {
			msg, err := tfjson.UnmarshalLogMessage(scanner.Bytes())
			if !yield(tfexec.NextMessage{Msg: msg, Err: err}) {
				return
			}
		}
		yield(tfexec.NextMessage{Err: f.exit})
	}, nil
}

// awsSchemas has the list resources of the types the recorded output
// lists, and one that needs arguments.
func awsSchemas() *tfjson.ProviderSchemas {
	region := &tfjson.SchemaAttribute{AttributeType: cty.String, Optional: true}
	optional := func() *tfjson.Schema {
		return &tfjson.Schema{Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"region": region}}}
	}
	return &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ListResourceSchemas: map[string]*tfjson.Schema{
			"aws_cloudwatch_log_group": optional(),
			"aws_ebs_volume":           optional(),
			"aws_ecr_repository":       {Block: &tfjson.SchemaBlock{}},
			"aws_api_gateway_resource": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"rest_api_id": {AttributeType: cty.String, Required: true},
			}}},
		}},
	}}
}

func request(t *testing.T, types ...string) Request {
	return Request{
		ExecPath:     "terraform",
		Parent:       filepath.Join(t.TempDir(), ".infraharvest"),
		Provider:     rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Config: map[string]interface{}{"region": "eu-west-2"}},
		ProviderName: "aws",
		Types:        types,
		Config:       map[string]string{"region": "eu-west-2", "resource_group_name": "rg"},
		InScope:      func(identity map[string]string) bool { return identity["region"] == "eu-west-2" },
	}
}

// fakeRunner runs tf at version v.
func fakeRunner(t *testing.T, tf *fakeTerraform, v string) runner {
	return runner{
		newTerraform: func(dir, _, _ string, _ map[string]string) (terraform, error) {
			tf.dir = dir
			for _, name := range []string{rootfile.VersionsFileName, rootfile.ProvidersFileName} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Error(err)
				}
			}
			return tf, nil
		},
		versionOf: func(context.Context, string) (*version.Version, error) {
			return version.Must(version.NewVersion(v)), nil
		},
	}
}

// List writes a list block per type, identities only, with Limit and the
// scope's arguments its list resource takes; records what it finds in the
// scope by identity; and reports the type whose list failed and the one
// that reached Limit, keeping the others' results. Its working directory
// is gone afterwards.
func TestList(t *testing.T) {
	t.Parallel()
	output, err := os.ReadFile(filepath.Join("testdata", "query.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tf := &fakeTerraform{t: t, schemas: awsSchemas(), output: output, exit: errors.New("exit status 1")}
	req := request(t, "aws_cloudwatch_log_group", "aws_ebs_volume", "aws_ecr_repository")
	got, err := fakeRunner(t, tf, "1.16.6").list(context.Background(), req)

	wantBlocks := `list "aws_cloudwatch_log_group" "all" {
  provider         = aws
  include_resource = false
  limit            = 10000
  config {
    region = "eu-west-2"
  }
}

list "aws_ebs_volume" "all" {
  provider         = aws
  include_resource = false
  limit            = 10000
  config {
    region = "eu-west-2"
  }
}

list "aws_ecr_repository" "all" {
  provider         = aws
  include_resource = false
  limit            = 10000
}
`
	if tf.queried != wantBlocks {
		t.Errorf("list blocks:\n%s\nwant:\n%s", tf.queried, wantBlocks)
	}
	want := []harvest.Resource{
		harvest.RecordIdentity(map[string]string{"account_id": "123456789012", "name": "/app/one", "region": "eu-west-2"}, "/app/one", "aws_cloudwatch_log_group", "aws"),
		harvest.RecordIdentity(map[string]string{"account_id": "123456789012", "id": "vol-0abc", "region": "eu-west-2"}, "vol-0abc", "aws_ebs_volume", "aws"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !harvest.IsPartial(err) {
		t.Fatalf("want a partial error, got %v", err)
	}
	for _, part := range []string{"aws_ecr_repository: Error Listing Tags: An error occurred while listing tags for ecr: AccessDenied", "aws_ebs_volume: the list stopped at its limit of 10000 results"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q doesn't say %q", err, part)
		}
	}
	if _, err := os.Stat(tf.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("working directory %s is still there: %v", tf.dir, err)
	}
	if !strings.HasPrefix(tf.dir, req.Parent) {
		t.Errorf("working directory %s isn't under %s", tf.dir, req.Parent)
	}
}

// A listing whose lists all completed returns what they found, with no
// error; an error no list block owns fails the listing.
func TestListErrors(t *testing.T) {
	t.Parallel()
	complete := `{"@level":"info","@message":"list.aws_ebs_volume.all: Result found","@module":"terraform.ui","@timestamp":"2026-10-28T05:44:17.951000Z","list_resource_found":{"address":"list.aws_ebs_volume.all","display_name":"vol-1","identity":{"id":"vol-1","region":"eu-west-2"},"identity_version":0,"resource_type":"aws_ebs_volume"},"type":"list_resource_found"}
{"@level":"info","@message":"list.aws_ebs_volume.all: List complete","@module":"terraform.ui","@timestamp":"2026-10-28T05:44:17.960000Z","list_complete":{"address":"list.aws_ebs_volume.all","resource_type":"aws_ebs_volume","total":1},"type":"list_complete"}
`
	unowned := `{"@level":"error","@message":"Error: Invalid provider configuration","@module":"terraform.ui","@timestamp":"2026-10-28T05:44:24.592917Z","diagnostic":{"severity":"error","summary":"Invalid provider configuration","detail":"no credentials"},"type":"diagnostic"}
`
	for name, tt := range map[string]struct {
		output string
		exit   error
		found  int
		fails  bool
	}{
		"complete":        {output: complete, found: 1},
		"unowned error":   {output: complete + unowned, exit: errors.New("exit status 1"), fails: true},
		"failed silently": {output: "", exit: errors.New("exit status 1"), fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tf := &fakeTerraform{t: t, schemas: awsSchemas(), output: []byte(tt.output), exit: tt.exit}
			got, err := fakeRunner(t, tf, "1.14.0").list(context.Background(), request(t, "aws_ebs_volume"))
			if tt.fails {
				if err == nil || harvest.IsPartial(err) || got != nil {
					t.Errorf("want a full failure, got %d resources, %v", len(got), err)
				}
				return
			}
			if err != nil || len(got) != tt.found {
				t.Errorf("got %d resources, %v; want %d", len(got), err, tt.found)
			}
		})
	}
}

// Types the provider has no list resource for, and those whose list
// resource needs arguments, fail the listing before it queries, naming
// them; so does a Terraform older than 1.14.
func TestListRefuses(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		types   []string
		version string
		want    []string
	}{
		"no list resource": {[]string{"aws_ebs_volume", "aws_nothing", "aws_none"}, "1.16.0", []string{"hashicorp/aws has no list resource for aws_nothing, aws_none"}},
		"needs arguments":  {[]string{"aws_api_gateway_resource"}, "1.16.0", []string{"aws_api_gateway_resource (rest_api_id)"}},
		"old terraform":    {[]string{"aws_ebs_volume"}, "1.13.4", []string{"needs Terraform 1.14.0 or newer", "this is 1.13.4", "--terraform-path"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tf := &fakeTerraform{t: t, schemas: awsSchemas()}
			_, err := fakeRunner(t, tf, tt.version).list(context.Background(), request(t, tt.types...))
			if err == nil {
				t.Fatal("want an error")
			}
			for _, part := range tt.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q doesn't say %q", err, part)
				}
			}
			if tf.queried != "" {
				t.Error("it queried")
			}
		})
	}
}

// The binary a run lists with is Terraform, at the first release with
// terraform query.
func TestBinary(t *testing.T) {
	t.Parallel()
	if b := Binary(); b.Name != "terraform" || b.Minimum.String() != "1.14.0" || tfrun.TerraformBinary.Minimum.String() != "1.5.0" {
		t.Errorf("got %s %s; Terraform's own %s", b.Name, b.Minimum, tfrun.TerraformBinary.Minimum)
	}
}
