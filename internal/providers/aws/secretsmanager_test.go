// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister leaves out the secrets services create for themselves, such
// as the master password RDS manages for a database, and the secrets
// scheduled for deletion, which it asks not to be listed.
func TestSecretsManagerLeavesOutServiceSecrets(t *testing.T) {
	const arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:"
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListSecrets" {
			t.Errorf("unexpected call %s", call.Op)
			return `{"__type":"InvalidRequestException"}`
		}
		if planned, set := recordJSONBool(call, "IncludePlannedDeletion"); !set || planned {
			t.Errorf("secrets listed with those scheduled for deletion: %s", call.Body)
		}
		return `{"SecretList":[
			{"ARN":"` + arn + `app-AbCdEf","Name":"app"},
			{"ARN":"` + arn + `rds!db-1a2b-GhIjKl","Name":"rds!db-1a2b","OwningService":"rds"},
			{"ARN":"` + arn + `old-MnOpQr","Name":"old","DeletedDate":1767225600}]}`
	})
	g := &SecretsManagerLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_secretsmanager_secret", arn+"app-AbCdEf")
}
