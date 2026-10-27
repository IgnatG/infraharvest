// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

package main

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-aws/internal"
	"github.com/hashicorp/terraform-provider-aws/internal/provider/framework"
	"github.com/hashicorp/terraform-provider-aws/internal/provider/sdkv2"
)

// loadProvider builds the AWS provider's two halves as its own server
// factory does (internal/provider/factory.go), without serving them.
func loadProvider(ctx context.Context) (string, *schema.Provider, provider.Provider, error) {
	internal.RegisterSmarterrFS()
	primary, err := sdkv2.NewProvider(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	secondary, err := framework.NewProvider(ctx, primary)
	if err != nil {
		return "", nil, nil, err
	}
	return "registry.terraform.io/hashicorp/aws", primary, secondary, nil
}
