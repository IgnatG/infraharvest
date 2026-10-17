// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordKmsKeyARN = "arn:aws:kms:us-east-1:123456789012:key/"

// recordKmsKeys answers DescribeKey for the keys of the KMS tests: who
// manages each key and its state, or an error. The AWS managed key k-aws,
// which alias/aws/s3 names, has none: it must not be described.
var recordKmsKeys = map[string]string{
	"k-app":     `{"KeyMetadata":{"KeyId":"k-app","KeyManager":"CUSTOMER","KeyState":"Enabled"}}`,
	"k-doomed":  `{"KeyMetadata":{"KeyId":"k-doomed","KeyManager":"CUSTOMER","KeyState":"PendingDeletion"}}`,
	"k-replica": `{"KeyMetadata":{"KeyId":"k-replica","KeyManager":"CUSTOMER","KeyState":"PendingReplicaDeletion"}}`,
	"k-off":     `{"KeyMetadata":{"KeyId":"k-off","KeyManager":"CUSTOMER","KeyState":"Disabled"}}`,
	"k-mrr":     `{"KeyMetadata":{"KeyId":"k-mrr","KeyManager":"CUSTOMER","KeyState":"Enabled","Origin":"AWS_KMS","MultiRegion":true,"MultiRegionConfiguration":{"MultiRegionKeyType":"REPLICA"}}}`,
	"k-secret":  recordJSONError("AccessDeniedException"),
}

// The lister lists the customer managed keys not being deleted, each
// followed by its grants, then the aliases of such keys. Keys and aliases it
// can't describe are left out, each key described once; a key whose grants
// it can't list is still listed; and both failures make the listing
// partial. Grants are imported as <grant's key ID>:<grant ID>, and the grant's
// key ID is the key's ARN. A key is named after its alias, and a grant
// after its name: before, they were labelled after their IDs. A grant
// records its grantee principal, by which the default selection leaves out
// the grants AWS services make for themselves. An AWS
// managed alias, alias/aws/<service>, is left out without describing its
// key, and so is that key when the keys are listed.
func TestRecordKmsListsCustomerKeysGrantsAndAliases(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListKeys":
			if recordSecondPage(call) {
				return `{"Keys":[{"KeyId":"k-replica"},{"KeyId":"k-secret"},{"KeyId":"k-off"},{"KeyId":"k-mrr"}],"Truncated":false}`
			}
			return `{"Keys":[{"KeyId":"k-app"},{"KeyId":"k-aws"},{"KeyId":"k-doomed"}],"NextMarker":"` + nextPageToken + `","Truncated":true}`
		case "DescribeKey":
			if answer, ok := recordKmsKeys[recordJSONField(call, "KeyId")]; ok {
				return answer
			}
		case "ListGrants":
			switch recordJSONField(call, "KeyId") {
			case "k-app":
				id, token := recordPage(call, "grant")
				return fmt.Sprintf(`{"Grants":[{"KeyId":"%sk-app","GrantId":"%s","Name":"lambda","GranteePrincipal":"arn:aws:iam::123456789012:role/app"}],"NextMarker":"%s","Truncated":%t}`, recordKmsKeyARN, id, token, token != "")
			case "k-off":
				return recordJSONError("AccessDeniedException")
			case "k-mrr":
				return `{"Grants":[],"Truncated":false}`
			}
		case "ListAliases":
			if recordSecondPage(call) {
				return `{"Aliases":[
					{"AliasName":"alias/old","TargetKeyId":"k-doomed"},
					{"AliasName":"alias/hidden","TargetKeyId":"k-secret"},
					{"AliasName":"alias/off","TargetKeyId":"k-off"}],"Truncated":false}`
			}
			return `{"Aliases":[
				{"AliasName":"alias/aws/s3","TargetKeyId":"k-aws"},
				{"AliasName":"alias/unused"},
				{"AliasName":"alias/app","TargetKeyId":"k-app"}],"NextMarker":"` + nextPageToken + `","Truncated":true}`
		}
		return recordUnexpected(t, call)
	})
	g := &KmsLister{}

	err := g.List()
	if err == nil {
		t.Fatal("want a partial error, got none")
	}
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "key k-secret") || !strings.Contains(err.Error(), "grants of key k-off") {
		t.Errorf("want a partial error for the key not described and the grants not listed, got %v", err)
	}
	if n := strings.Count(err.Error(), "key k-secret:"); n != 1 {
		t.Errorf("k-secret's failure reported %d times, want once: %v", n, err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_kms_key", "k-app", "app", map[string]string{"key_id": "k-app"}},
		recordedResource{"aws_kms_grant", recordKmsKeyARN + "k-app:grant-1", "lambda", map[string]string{"grantee_principal": "arn:aws:iam::123456789012:role/app"}},
		recordedResource{"aws_kms_grant", recordKmsKeyARN + "k-app:grant-2", "lambda", map[string]string{"grantee_principal": "arn:aws:iam::123456789012:role/app"}},
		recordedResource{"aws_kms_key", "k-off", "off", map[string]string{"key_id": "k-off"}},
		// A multi-Region replica is an aws_kms_replica_key.
		recordedResource{"aws_kms_replica_key", "k-mrr", "k-mrr", map[string]string{"key_id": "k-mrr"}},
		recordedResource{"aws_kms_alias", "alias/app", "alias/app", nil},
		recordedResource{"aws_kms_alias", "alias/off", "alias/off", nil},
	)
}

// Listing keys or aliases fails the service.
func TestRecordKmsReturnsListErrors(t *testing.T) {
	for _, failing := range []string{"ListKeys", "ListAliases"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListKeys":
					return `{"Keys":[]}`
				case "ListAliases":
					return `{"Aliases":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &KmsLister{}

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}

// A key's type follows the origin of its material and whether it is a
// multi-Region replica, from the metadata DescribeKey answered.
func TestKmsKeyType(t *testing.T) {
	replica := &types.MultiRegionConfiguration{MultiRegionKeyType: types.MultiRegionKeyTypeReplica}
	primary := &types.MultiRegionConfiguration{MultiRegionKeyType: types.MultiRegionKeyTypePrimary}
	for _, c := range []struct {
		metadata types.KeyMetadata
		want     string
	}{
		{types.KeyMetadata{Origin: types.OriginTypeAwsKms}, "aws_kms_key"},
		{types.KeyMetadata{}, "aws_kms_key"},
		{types.KeyMetadata{Origin: types.OriginTypeAwsCloudhsm}, "aws_kms_key"},
		{types.KeyMetadata{Origin: types.OriginTypeAwsKms, MultiRegionConfiguration: primary}, "aws_kms_key"},
		{types.KeyMetadata{Origin: types.OriginTypeAwsKms, MultiRegionConfiguration: replica}, "aws_kms_replica_key"},
		{types.KeyMetadata{Origin: types.OriginTypeExternal}, "aws_kms_external_key"},
		{types.KeyMetadata{Origin: types.OriginTypeExternal, MultiRegionConfiguration: primary}, "aws_kms_external_key"},
		{types.KeyMetadata{Origin: types.OriginTypeExternal, MultiRegionConfiguration: replica}, "aws_kms_replica_external_key"},
	} {
		if got := kmsKeyType(&c.metadata); got != c.want {
			t.Errorf("origin %q, multi-Region %+v: got %s, want %s", c.metadata.Origin, c.metadata.MultiRegionConfiguration, got, c.want)
		}
	}
}
