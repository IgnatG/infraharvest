// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// S3Bucket maps a bucket and its configuration resources onto
// terraform-aws-modules/s3-bucket/aws. It declines buckets with
// configuration it doesn't map yet, such as logging, CORS or replication.
var S3Bucket = Adapter{
	Source:  "terraform-aws-modules/s3-bucket/aws",
	Version: "5.16.1",
	Anchor:  "aws_s3_bucket",
	Members: []string{
		"aws_s3_bucket_lifecycle_configuration",
		"aws_s3_bucket_ownership_controls",
		"aws_s3_bucket_policy",
		"aws_s3_bucket_public_access_block",
		"aws_s3_bucket_server_side_encryption_configuration",
		"aws_s3_bucket_versioning",
	},
	Inputs: []string{
		"attach_policy", "attach_public_policy", "block_public_acls", "block_public_policy",
		"bucket", "bucket_namespace", "control_object_ownership", "force_destroy",
		"ignore_public_acls", "lifecycle_rule", "object_lock_enabled", "object_ownership", "policy",
		"restrict_public_buckets", "server_side_encryption_configuration",
		"skip_destroy_public_access_block", "tags", "transition_default_minimum_object_size", "versioning",
	},
	Outputs: []string{
		"s3_bucket_arn", "s3_bucket_bucket_domain_name", "s3_bucket_bucket_namespace",
		"s3_bucket_bucket_regional_domain_name", "s3_bucket_hosted_zone_id", "s3_bucket_id", "s3_bucket_region",
	},
	Map: mapS3Bucket,
}

func mapS3Bucket(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "aws_s3_bucket.this[0]"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"arn":                         "s3_bucket_arn",
			"bucket":                      "s3_bucket_id",
			"bucket_domain_name":          "s3_bucket_bucket_domain_name",
			"bucket_namespace":            "s3_bucket_bucket_namespace",
			"bucket_regional_domain_name": "s3_bucket_bucket_regional_domain_name",
			"hosted_zone_id":              "s3_bucket_hosted_zone_id",
			"id":                          "s3_bucket_id",
			"region":                      "s3_bucket_region",
		}},
	}
	bucket := Read(c.Anchor.Address(), c.Anchor.Body)
	name, ok := bucket.Attr("bucket")
	if !ok {
		return nil, Declinef("%s has no bucket name", c.Anchor.Address())
	}
	call.Set("bucket", name)
	for _, arg := range []string{"bucket_namespace", "force_destroy", "object_lock_enabled"} {
		if value, ok := bucket.Attr(arg); ok {
			call.Set(arg, value)
		}
	}
	// Untagged as in the root, where the provider's default tags apply:
	// the module's default is an empty map.
	tags, ok := bucket.Attr("tags")
	if !ok {
		tags = hclwrite.TokensForValue(cty.NullVal(cty.Map(cty.String)))
	}
	call.Set("tags", tags)
	if err := bucket.Done(); err != nil {
		return nil, err
	}

	publicAccessBlock := false
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		if !r.RefersTo("bucket", c.Anchor) {
			return nil, Declinef("%s doesn't refer to %s", m.Address(), c.Anchor.Address())
		}
		// The module sets one expected owner for every resource.
		if _, ok := r.Attr("expected_bucket_owner"); ok {
			return nil, Declinef("%s sets expected_bucket_owner", m.Address())
		}
		var err error
		switch m.Type {
		case "aws_s3_bucket_versioning":
			err = s3Versioning(call, r)
		case "aws_s3_bucket_server_side_encryption_configuration":
			err = s3Encryption(call, r)
		case "aws_s3_bucket_public_access_block":
			publicAccessBlock = true
			s3PublicAccessBlock(call, r)
		case "aws_s3_bucket_ownership_controls":
			err = s3OwnershipControls(call, r)
		case "aws_s3_bucket_lifecycle_configuration":
			err = s3Lifecycle(call, r)
		case "aws_s3_bucket_policy":
			if policy, ok := r.Attr("policy"); ok {
				call.Set("attach_policy", hclwrite.TokensForValue(cty.True))
				call.Set("policy", policy)
			}
		}
		if err != nil {
			return nil, err
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
		call.Addresses[m.Address()] = m.Type + ".this[0]"
	}
	// The module creates a public access block unless told not to.
	if !publicAccessBlock {
		call.Set("attach_public_policy", hclwrite.TokensForValue(cty.False))
	}
	return call, nil
}

// s3Versioning maps versioning onto the module's versioning map. The
// module reads "status" and "mfa_delete" as Enabled, Suspended or Disabled.
func s3Versioning(call *Call, r *Reader) error {
	if _, ok := r.Attr("mfa"); ok {
		return Declinef("versioning sets mfa")
	}
	config, err := r.Block("versioning_configuration")
	if err != nil {
		return err
	}
	if config == nil {
		return Declinef("versioning has no versioning_configuration")
	}
	var versioning Object
	versioning.Copy(config, "status", "mfa_delete")
	call.Set("versioning", versioning.Tokens())
	return nil
}

// s3Encryption maps the encryption configuration's one rule.
func s3Encryption(call *Call, r *Reader) error {
	rule, err := r.Block("rule")
	if err != nil {
		return err
	}
	if rule == nil {
		return Declinef("encryption has no rule")
	}
	var ruleObject Object
	ruleObject.Copy(rule, "bucket_key_enabled", "blocked_encryption_types")
	byDefault, err := rule.Block("apply_server_side_encryption_by_default")
	if err != nil {
		return err
	}
	if byDefault != nil {
		var o Object
		o.Copy(byDefault, "sse_algorithm", "kms_master_key_id")
		ruleObject.Set("apply_server_side_encryption_by_default", o.Tokens())
	}
	var config Object
	config.Set("rule", ruleObject.Tokens())
	call.Set("server_side_encryption_configuration", config.Tokens())
	return nil
}

// s3PublicAccessBlock passes the four settings explicitly: the module's
// defaults are true, the resource's false.
func s3PublicAccessBlock(call *Call, r *Reader) {
	for _, arg := range []string{"block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"} {
		value, ok := r.Attr(arg)
		if !ok {
			value = hclwrite.TokensForValue(cty.False)
		}
		call.Set(arg, value)
	}
	// skip_destroy is kept only in state: pass what the import has.
	value, ok := r.Attr("skip_destroy")
	if !ok {
		value = hclwrite.TokensForValue(cty.NullVal(cty.Bool))
	}
	call.Set("skip_destroy_public_access_block", value)
}

func s3OwnershipControls(call *Call, r *Reader) error {
	rule, err := r.Block("rule")
	if err != nil {
		return err
	}
	if rule == nil {
		return Declinef("ownership controls have no rule")
	}
	ownership, ok := rule.Attr("object_ownership")
	if !ok {
		return Declinef("ownership controls have no object_ownership")
	}
	call.Set("control_object_ownership", hclwrite.TokensForValue(cty.True))
	call.Set("object_ownership", ownership)
	return nil
}

// s3Lifecycle maps each rule onto an object of the module's lifecycle_rule
// list, whose single-block settings are objects and repeated ones lists.
func s3Lifecycle(call *Call, r *Reader) error {
	if value, ok := r.Attr("transition_default_minimum_object_size"); ok {
		call.Set("transition_default_minimum_object_size", value)
	}
	var rules []hclwrite.Tokens
	for _, rule := range r.Blocks("rule") {
		// The module doesn't pass a rule's own prefix (deprecated).
		if _, ok := rule.Attr("prefix"); ok {
			return Declinef("a lifecycle rule sets prefix")
		}
		var o Object
		o.Copy(rule, "id", "status")
		abort, err := rule.Block("abort_incomplete_multipart_upload")
		if err != nil {
			return err
		}
		if abort != nil {
			if days, ok := abort.Attr("days_after_initiation"); ok {
				o.Set("abort_incomplete_multipart_upload_days", days)
			}
		}
		for _, single := range []struct {
			block string
			args  []string
		}{
			{"expiration", []string{"date", "days", "expired_object_delete_marker"}},
			{"noncurrent_version_expiration", []string{"newer_noncurrent_versions", "noncurrent_days"}},
		} {
			b, err := rule.Block(single.block)
			if err != nil {
				return err
			}
			if b != nil {
				var inner Object
				inner.Copy(b, single.args...)
				o.Set(single.block, inner.Tokens())
			}
		}
		for _, repeated := range []struct {
			block string
			args  []string
		}{
			{"transition", []string{"date", "days", "storage_class"}},
			{"noncurrent_version_transition", []string{"newer_noncurrent_versions", "noncurrent_days", "storage_class"}},
		} {
			var items []hclwrite.Tokens
			for _, b := range rule.Blocks(repeated.block) {
				var inner Object
				inner.Copy(b, repeated.args...)
				items = append(items, inner.Tokens())
			}
			if len(items) > 0 {
				o.Set(repeated.block, hclwrite.TokensForTuple(items))
			}
		}
		filter, err := s3LifecycleFilter(rule)
		if err != nil {
			return err
		}
		if filter != nil {
			o.Set("filter", filter)
		}
		rules = append(rules, o.Tokens())
	}
	if len(rules) > 0 {
		call.Set("lifecycle_rule", hclwrite.TokensForTuple(rules))
	}
	return nil
}

// s3LifecycleFilter maps a rule's filter onto one object: the module
// writes the and block itself when more than one condition is set.
func s3LifecycleFilter(rule *Reader) (hclwrite.Tokens, error) {
	filter, err := rule.Block("filter")
	if err != nil || filter == nil {
		return nil, err
	}
	conditions := []string{"object_size_greater_than", "object_size_less_than", "prefix"}
	var o Object
	o.Copy(filter, conditions...)
	tag, err := filter.Block("tag")
	if err != nil {
		return nil, err
	}
	if tag != nil {
		key, hasKey := tag.Attr("key")
		value, hasValue := tag.Attr("value")
		if !hasKey || !hasValue {
			return nil, Declinef("a lifecycle filter tag has no key or value")
		}
		o.Set("tags", hclwrite.TokensForObject([]hclwrite.ObjectAttrTokens{{Name: parenthesized(key), Value: value}}))
	}
	and, err := filter.Block("and")
	if err != nil {
		return nil, err
	}
	if and != nil {
		if !o.Empty() {
			return nil, Declinef("a lifecycle filter has conditions in and outside its and block")
		}
		o.Copy(and, append(conditions, "tags")...)
	}
	return o.Tokens(), nil
}

// parenthesized makes an expression usable as an object key: (key).
func parenthesized(tokens hclwrite.Tokens) hclwrite.Tokens {
	out := hclwrite.Tokens{{Type: hclsyntax.TokenOParen, Bytes: []byte("(")}}
	out = append(out, tokens...)
	return append(out, &hclwrite.Token{Type: hclsyntax.TokenCParen, Bytes: []byte(")")})
}
