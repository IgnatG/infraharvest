// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// trustCaller lets the emulator's default caller, the account
// 000000000000, assume a role. Floci checks trust policies since 2.2.0.
const trustCaller = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`

// withRole creates the role name in the emulator's account for the length
// of check, then deletes it, so that the imports around it see the estate
// testdata/aws creates.
func withRole(ctx context.Context, t *testing.T, name string, check func()) {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc := iam.NewFromConfig(cfg)
	if _, err := svc.CreateRole(ctx, &iam.CreateRoleInput{RoleName: awssdk.String(name), AssumeRolePolicyDocument: awssdk.String(trustCaller)}); err != nil {
		t.Fatalf("create role %s: %v", name, err)
	}
	defer func() {
		if _, err := svc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: awssdk.String(name)}); err != nil {
			t.Errorf("delete role %s: %v", name, err)
		}
	}()
	check()
}
