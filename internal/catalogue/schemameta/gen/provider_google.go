// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

package main

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-google/google/fwprovider"
	google "github.com/hashicorp/terraform-provider-google/google/provider"
)

// loadProvider builds the Google provider's two halves as its main.go
// does, without serving them.
func loadProvider(context.Context) (string, *schema.Provider, provider.Provider, error) {
	primary := google.Provider()
	return "registry.terraform.io/hashicorp/google", primary, fwprovider.New(primary), nil
}
