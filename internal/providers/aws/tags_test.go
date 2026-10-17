// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestTagIndexLookup(t *testing.T) {
	x := newTagIndex(awsCatalogue())
	for arn, env := range map[string]string{
		"arn:aws:ec2:eu-west-2:123456789012:vpc/vpc-1":                                               "vpc",
		"arn:aws:s3:::logs":                                                                          "bucket",
		"arn:aws:sqs:eu-west-2:123456789012:jobs":                                                    "queue",
		"arn:aws:sns:eu-west-2:123456789012:alerts":                                                  "topic",
		"arn:aws:iam::123456789012:role/service/deployer":                                            "role",
		"arn:aws:lambda:eu-west-2:123456789012:function:resize":                                      "function",
		"arn:aws:logs:eu-west-2:123456789012:log-group:/app/web":                                     "log group",
		"arn:aws:rds:eu-west-2:123456789012:db:orders":                                               "db",
		"arn:aws:apigateway:eu-west-2::/restapis/a1b2":                                               "api",
		"arn:aws:ssm:eu-west-2:123456789012:parameter/app/token":                                     "parameter",
		"arn:aws:dynamodb:eu-west-2:123456789012:table/orders":                                       "table",
		"arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/web":                       "classic",
		"arn:aws:autoscaling:eu-west-2:123456789012:autoScalingGroup:u:autoScalingGroupName/workers": "asg",
	} {
		x.add(arn, map[string]string{"env": env})
	}

	for _, tc := range []struct {
		typ, id, importID string
		attributes        map[string]string
		want              string
	}{
		{"aws_vpc", "vpc-1", "", nil, "vpc"},
		{"aws_s3_bucket", "logs", "", nil, "bucket"},
		{"aws_sqs_queue", "https://sqs.eu-west-2.amazonaws.com/123456789012/jobs", "", nil, "queue"},
		{"aws_sns_topic", "arn:aws:sns:eu-west-2:123456789012:alerts", "", nil, "topic"},
		{"aws_iam_role", "deployer", "", nil, "role"},
		{"aws_lambda_function", "resize", "", nil, "function"},
		{"aws_cloudwatch_log_group", "/app/web", "", nil, "log group"},
		{"aws_db_instance", "orders", "", nil, "db"},
		{"aws_api_gateway_rest_api", "a1b2", "", nil, "api"},
		{"aws_ssm_parameter", "/app/token", "", nil, "parameter"},
		{"aws_dynamodb_table", "orders", "", nil, "table"},
		{"aws_elb", "web", "", nil, "classic"},
		{"aws_autoscaling_group", "workers", "", nil, "asg"},
		// Found through the import ID or the arn attribute.
		{"aws_sns_topic", "alerts-topic", "arn:aws:sns:eu-west-2:123456789012:alerts", nil, "topic"},
		{"aws_something", "x", "", map[string]string{"arn": "arn:aws:s3:::logs"}, "bucket"},
		// The table doesn't confuse types of the same name.
		{"aws_rds_cluster", "orders", "", nil, ""},
		// Types it doesn't know are found by ARN only.
		{"aws_s3_bucket_versioning", "logs", "", nil, ""},
		{"aws_vpc", "vpc-2", "", nil, ""},
	} {
		r := harvest.Record(tc.id, tc.id, tc.typ, "aws", tc.attributes)
		tags, ok := x.lookup(r, tc.importID)
		if got := tags["env"]; got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s %s: got %q (%v), want %q", tc.typ, tc.id, got, ok, tc.want)
		}
	}
}

// A name that two ARNs share says neither.
func TestTagIndexAmbiguousName(t *testing.T) {
	x := newTagIndex(awsCatalogue())
	x.add("arn:aws:iam::123456789012:role/a/deployer", map[string]string{"env": "a"})
	x.add("arn:aws:iam::123456789012:role/b/deployer", map[string]string{"env": "b"})
	if tags, ok := x.lookup(harvest.RecordBare("deployer", "deployer", "aws_iam_role", "aws"), ""); ok {
		t.Errorf("got %v, want no tags", tags)
	}
	if tags, ok := x.lookup(harvest.RecordBare("a/deployer", "deployer", "aws_iam_role", "aws"), ""); !ok || tags["env"] != "a" {
		t.Errorf("by path: got %v, %v", tags, ok)
	}
}

// The provider reads the catalogue of the import, the --catalogue overlay
// merged in, once SetCatalogue gives it: an overlay's arn entry finds a
// type's tags, and its import ID is what the provider matches on.
func TestProviderUsesTheImportsCatalogue(t *testing.T) {
	dir := t.TempDir()
	overlay := "provider: aws\ntypes:\n  aws_iot_thing:\n    arn: { service: iot, resource_type: thing }\n" +
		"  aws_route_table_association:\n    import_id: \"{id}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "aws.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("aws", dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{}
	if p.catalogue() != awsCatalogue() {
		t.Error("before SetCatalogue, want the built-in catalogue")
	}
	p.SetCatalogue(cat)

	thing := harvest.RecordBare("sensor", "sensor", "aws_iot_thing", "aws")
	for _, c := range []struct {
		cat  *catalogue.Catalogue
		want bool
	}{{awsCatalogue(), false}, {p.catalogue(), true}} {
		x := newTagIndex(c.cat)
		x.add("arn:aws:iot:eu-west-2:123456789012:thing/sensor", map[string]string{"env": "iot"})
		if _, ok := x.lookup(thing, ""); ok != c.want {
			t.Errorf("thing found by name: %v, want %v (only the overlay has its arn entry)", ok, c.want)
		}
	}
	association := harvest.Record("rtbassoc-1", "subnet-1", "aws_route_table_association", "aws",
		map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1"})
	if id, ok := importID(p.catalogue(), association); id != "rtbassoc-1" || !ok {
		t.Errorf("an overlay's import ID: got %q, %v", id, ok)
	}
}
