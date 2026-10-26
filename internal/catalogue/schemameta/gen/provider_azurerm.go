// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

package main

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	azurerm "github.com/hashicorp/terraform-provider-azurerm/internal/provider"
	"github.com/hashicorp/terraform-provider-azurerm/internal/provider/framework"
)

// loadProvider builds the AzureRM provider's two halves as its own server
// factory does (internal/provider/framework/factory_builder.go), without
// serving them.
func loadProvider(context.Context) (string, *schema.Provider, provider.Provider, error) {
	primary := azurerm.AzureProvider()
	return "registry.terraform.io/hashicorp/azurerm", primary, framework.NewFrameworkProvider(primary), nil
}
