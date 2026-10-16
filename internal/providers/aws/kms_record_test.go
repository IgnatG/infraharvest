// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
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
	"k-secret":  recordJSONError("AccessDeniedException"),
}

// The lister lists the customer managed keys not being deleted, each
// followed by its grants, then the aliases of such keys. Keys and aliases it
// can't describe are left out; a key whose grants it can't list is still
// listed. Grants are imported as <grant's key ID>:<grant ID>, and the grant's
// key ID is the key's ARN. A key is named after its alias, and a grant
// after its name: before, they were labelled after their IDs. An AWS
// managed alias, alias/aws/<service>, is left out without describing its
// key, and so is that key when the keys are listed.
func TestRecordKmsListsCustomerKeysGrantsAndAliases(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListKeys":
			if recordSecondPage(call) {
				return `{"Keys":[{"KeyId":"k-replica"},{"KeyId":"k-secret"},{"KeyId":"k-off"}],"Truncated":false}`
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
				return fmt.Sprintf(`{"Grants":[{"KeyId":"%sk-app","GrantId":"%s","Name":"lambda"}],"NextMarker":"%s","Truncated":%t}`, recordKmsKeyARN, id, token, token != "")
			case "k-off":
				return recordJSONError("AccessDeniedException")
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

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_kms_key", "k-app", "app", map[string]string{"key_id": "k-app"}},
		recordedResource{"aws_kms_grant", recordKmsKeyARN + "k-app:grant-1", "lambda", nil},
		recordedResource{"aws_kms_grant", recordKmsKeyARN + "k-app:grant-2", "lambda", nil},
		recordedResource{"aws_kms_key", "k-off", "off", map[string]string{"key_id": "k-off"}},
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
