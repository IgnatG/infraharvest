// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// googleStorageBucket maps a Cloud Storage bucket onto the simple_bucket
// module of terraform-google-modules/cloud-storage/google. The module's
// defaults turn versioning and uniform bucket-level access on, so the
// adapter always passes what the bucket has. It declines buckets with
// settings the module has no input for, such as requester pays, object
// retention or an IP filter, and lifecycle conditions it can't express.
// The module always sets versioning, the soft delete policy and public
// access prevention: a bucket whose versioning was never set, which
// Google reports without it, plans a change in the module, and the plan
// check keeps the bucket in the root.
var googleStorageBucket = Adapter{
	Source:  "terraform-google-modules/cloud-storage/google//modules/simple_bucket",
	Version: "12.4.1",
	Anchor:  "google_storage_bucket",
	Inputs: []string{
		"autoclass", "bucket_policy_only", "cors", "custom_placement_config", "encryption", "force_destroy",
		"hierarchical_namespace", "labels", "lifecycle_rules", "location", "log_bucket", "log_object_prefix", "name",
		"project_id", "public_access_prevention", "retention_policy", "soft_delete_policy", "storage_class",
		"versioning", "website",
	},
	Outputs:   []string{"bucket", "name", "url"},
	Providers: []string{"google"},
	Targets:   []string{"google_storage_bucket.bucket"},
	Map:       mapGoogleStorageBucket,
	// The module's variables' defaults, from its variables.tf.
	Defaults: map[string]cty.Value{
		"autoclass":                cty.False,
		"bucket_policy_only":       cty.True,
		"force_destroy":            cty.False,
		"hierarchical_namespace":   cty.False,
		"public_access_prevention": cty.StringVal("inherited"),
		"versioning":               cty.True,
	},
	// The module sets these from the inputs as they are
	// (modules/simple_bucket/main.tf).
	Inverse: map[string]string{
		"google_storage_bucket.bucket.autoclass[0].enabled":        "autoclass",
		"google_storage_bucket.bucket.labels":                      "labels",
		"google_storage_bucket.bucket.public_access_prevention":    "public_access_prevention",
		"google_storage_bucket.bucket.storage_class":               "storage_class",
		"google_storage_bucket.bucket.uniform_bucket_level_access": "bucket_policy_only",
		"google_storage_bucket.bucket.versioning[0].enabled":       "versioning",
	},
}

func mapGoogleStorageBucket(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "google_storage_bucket.bucket"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"id":        "name",
			"name":      "name",
			"self_link": "bucket.self_link",
			"url":       "url",
		}},
	}
	b := Read(c.Anchor.Address(), c.Anchor.Body)
	if err := b.Require(call, Pair{"name", "name"}, Pair{"project", "project_id"}, Pair{"location", "location"}); err != nil {
		return nil, err
	}
	b.CopyAs(call,
		Pair{"force_destroy", "force_destroy"},
		Pair{"labels", "labels"},
		Pair{"public_access_prevention", "public_access_prevention"},
		Pair{"storage_class", "storage_class"},
	)
	// Terraform's own setting, which an import records at its default.
	b.IgnoreDeletionPolicy()
	// The module's default is true; left out, the bucket's is false.
	uniform, ok := b.Attr("uniform_bucket_level_access")
	if !ok {
		uniform = hclwrite.TokensForValue(cty.False)
	}
	call.Set("bucket_policy_only", uniform)
	// The module has no input for these: only their defaults map.
	for _, d := range []struct {
		arg  string
		want cty.Value
	}{
		{"default_event_based_hold", cty.False},
		{"enable_object_retention", cty.False},
		{"requester_pays", cty.False},
		{"rpo", cty.StringVal("DEFAULT")},
	} {
		if value, ok := b.Attr(d.arg); ok {
			if v, isLiteral := literal(value); !isLiteral || !v.RawEquals(d.want) {
				return nil, Declinef("%s sets %s, which the module can't", c.Anchor.Address(), d.arg)
			}
		}
	}

	versioning := hclwrite.TokensForValue(cty.False)
	if block, err := b.Block("versioning"); err != nil {
		return nil, err
	} else if block != nil {
		if enabled, ok := block.Attr("enabled"); ok {
			versioning = enabled
		}
	}
	call.Set("versioning", versioning)

	if block, err := b.Block("autoclass"); err != nil {
		return nil, err
	} else if block != nil {
		if enabled, ok := block.Attr("enabled"); ok {
			call.Set("autoclass", enabled)
		}
		// Google's default when autoclass is on; the module can't set another.
		if class, ok := block.Attr("terminal_storage_class"); ok {
			if v, _ := literalString(class); v != "NEARLINE" {
				return nil, Declinef("%s has autoclass end in %s, which the module can't set", c.Anchor.Address(), string(class.Bytes()))
			}
		}
	}
	if block, err := b.Block("hierarchical_namespace"); err != nil {
		return nil, err
	} else if block != nil {
		if enabled, ok := block.Attr("enabled"); ok {
			call.Set("hierarchical_namespace", enabled)
		}
	}
	// The module always sets a soft delete policy: pass the bucket's.
	if block, err := b.Block("soft_delete_policy"); err != nil {
		return nil, err
	} else if block != nil {
		var o Object
		o.Copy(block, "retention_duration_seconds")
		block.Ignore("effective_time")
		call.Set("soft_delete_policy", o.Tokens())
	}
	for _, nested := range []struct {
		block, input string
		args         []string
	}{
		{"retention_policy", "retention_policy", []string{"is_locked", "retention_period"}},
		{"encryption", "encryption", []string{"default_kms_key_name"}},
		{"custom_placement_config", "custom_placement_config", []string{"data_locations"}},
		{"website", "website", []string{"main_page_suffix", "not_found_page"}},
	} {
		block, err := b.Block(nested.block)
		if err != nil {
			return nil, err
		}
		if block != nil {
			var o Object
			o.Copy(block, nested.args...)
			call.Set(nested.input, o.Tokens())
		}
	}
	if block, err := b.Block("logging"); err != nil {
		return nil, err
	} else if block != nil {
		if value, ok := block.Attr("log_bucket"); ok {
			call.Set("log_bucket", value)
		}
		if value, ok := block.Attr("log_object_prefix"); ok {
			call.Set("log_object_prefix", value)
		}
	}
	var cors []hclwrite.Tokens
	for _, block := range b.Blocks("cors") {
		var o Object
		o.Copy(block, "max_age_seconds", "method", "origin", "response_header")
		cors = append(cors, o.Tokens())
	}
	if cors != nil {
		call.Set("cors", hclwrite.TokensForTuple(cors))
	}
	var rules []hclwrite.Tokens
	for _, block := range b.Blocks("lifecycle_rule") {
		rule, err := googleLifecycleRule(c.Anchor.Address(), block)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if rules != nil {
		call.Set("lifecycle_rules", hclwrite.TokensForTuple(rules))
	}
	if err := b.Done(); err != nil {
		return nil, err
	}
	return call, nil
}

// googleLifecycleRule maps a lifecycle rule onto an element of the
// module's lifecycle_rules, which takes the storage classes, prefixes and
// suffixes a condition matches as comma-separated strings.
func googleLifecycleRule(address string, rule *Reader) (hclwrite.Tokens, error) {
	action, err := rule.Block("action")
	if err != nil || action == nil {
		return nil, Declinef("%s has a lifecycle rule without one action", address)
	}
	var a Object
	a.Copy(action, "type", "storage_class")
	condition, err := rule.Block("condition")
	if err != nil {
		return nil, err
	}
	var cond Object
	if condition != nil {
		cond.Copy(condition, "age", "created_before", "custom_time_before", "days_since_custom_time",
			"days_since_noncurrent_time", "noncurrent_time_before", "num_newer_versions", "send_age_if_zero", "with_state")
		for _, arg := range []string{"matches_prefix", "matches_storage_class", "matches_suffix"} {
			value, ok := condition.Attr(arg)
			if !ok {
				continue
			}
			v, isLiteral := literal(value)
			if !isLiteral || (!v.Type().IsTupleType() && !v.Type().IsListType()) {
				return nil, Declinef("%s has a lifecycle condition %s that isn't a literal list", address, arg)
			}
			var items []string
			for it := v.ElementIterator(); it.Next(); {
				_, item := it.Element()
				if item.Type() != cty.String || strings.Contains(item.AsString(), ",") {
					return nil, Declinef("%s has a lifecycle condition %s the module can't join with commas", address, arg)
				}
				items = append(items, item.AsString())
			}
			if len(items) > 0 {
				cond.Set(arg, hclwrite.TokensForValue(cty.StringVal(strings.Join(items, ","))))
			}
		}
		// The module can't set these but to their defaults.
		for _, arg := range []string{"send_days_since_custom_time_if_zero", "send_days_since_noncurrent_time_if_zero", "send_num_newer_versions_if_zero"} {
			if value, ok := condition.Attr(arg); ok {
				if set, isLiteral := literalBool(value); !isLiteral || set {
					return nil, Declinef("%s has a lifecycle condition with %s", address, arg)
				}
			}
		}
	}
	var o Object
	o.Set("action", a.Tokens())
	o.Set("condition", cond.Tokens())
	return o.Tokens(), nil
}
