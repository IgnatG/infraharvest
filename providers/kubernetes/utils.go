// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kubernetes

import (
	"github.com/iancoleman/strcase"
)

// terraformType returns the Terraform resource type of a Kubernetes kind:
// "kubernetes_" plus the kind in snake case (Deployment is
// kubernetes_deployment). When the provider has no such type but has its
// "_v1" version, that is the type (DaemonSet is kubernetes_daemon_set_v1).
// It reports false when the provider has neither.
func terraformType(kind string) (string, bool) {
	tfType := "kubernetes_" + strcase.ToSnake(kind)
	if _, ok := supportedResourceTypes[tfType]; ok {
		return tfType, true
	}
	if _, ok := supportedResourceTypes[tfType+"_v1"]; ok {
		return tfType + "_v1", true
	}
	return "", false
}
