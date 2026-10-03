// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// ImportID returns the ID Terraform imports r with, where it differs from
// the ID the lister records, and false for types Terraform can't import.
func (AWSProvider) ImportID(r terraformutils.Resource) (string, bool) {
	switch r.InstanceInfo.Type {
	case "aws_main_route_table_association":
		// No import support; the VPC's main_route_table_id records it.
		return "", false
	case "aws_route_table_association":
		attrs := r.InstanceState.Attributes
		return attrs["subnet_id"] + "/" + attrs["route_table_id"], true
	case "aws_ecs_cluster":
		// The lister records the ARN; Terraform imports by name and builds
		// the ARN itself.
		id := r.InstanceState.ID
		return id[strings.LastIndex(id, "/")+1:], true
	case "aws_ecs_service":
		attrs := r.InstanceState.Attributes
		return attrs["cluster"] + "/" + attrs["name"], true
	case "aws_lambda_permission":
		attrs := r.InstanceState.Attributes
		return attrs["function_name"] + "/" + attrs["statement_id"], true
	case "aws_volume_attachment":
		attrs := r.InstanceState.Attributes
		return attrs["device_name"] + ":" + attrs["volume_id"] + ":" + attrs["instance_id"], true
	}
	return r.InstanceState.ID, true
}
