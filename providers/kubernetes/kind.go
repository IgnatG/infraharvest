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
	"fmt"
	"reflect"

	"github.com/IgnatG/infraharvest/terraformutils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Kind struct {
	KubernetesService
	Name       string
	Group      string
	Version    string
	Namespaced bool
}

// Generate TerraformResources from Kubernetes API,
// from each kubernetes object 1 TerraformResource.
// Use UID as the resource IDs.
func (k *Kind) InitResources() error {
	config, _, err := initClientAndConfig()
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	// The cluster names the group versions it prefers; client-go only has
	// clients for those it ships, which leaves out removed beta versions.
	groupName := extractClientSetFuncGroupName(k.Group, k.Version)
	groupMethod := reflect.ValueOf(clientset).MethodByName(groupName)
	if !groupMethod.IsValid() {
		return fmt.Errorf("%s %s/%s: no client for this API version", k.Name, k.Group, k.Version)
	}
	group := groupMethod.Call([]reflect.Value{})[0]

	param := []reflect.Value{}
	namespace := ""
	if k.Namespaced {
		param = append(param, reflect.ValueOf(namespace))
	}

	resourceMethod := group.MethodByName(extractClientSetFuncTypeName(k.Name))
	if !resourceMethod.IsValid() {
		return fmt.Errorf("%s %s/%s: no client for this kind", k.Name, k.Group, k.Version)
	}
	resource := resourceMethod.Call(param)[0]

	results := resource.MethodByName("List").Call([]reflect.Value{reflect.ValueOf(k.Context()),
		reflect.ValueOf(metav1.ListOptions{})})

	if !results[1].IsNil() {
		return results[1].Interface().(error)
	}
	items := reflect.Indirect(results[0]).FieldByName("Items")

	for i := 0; i < items.Len(); i++ {
		item := items.Index(i)
		// Filter to resources that aren't owned by any other resource
		if item.FieldByName("OwnerReferences").Len() > 0 {
			continue
		}

		name := ""
		if k.Namespaced {
			name = item.FieldByName("Namespace").String() + "/" + item.FieldByName("Name").String()
		} else {
			name = item.FieldByName("Name").String()
		}

		k.Resources = append(k.Resources, terraformutils.NewSimpleResource(
			name,
			name,
			extractTfResourceName(k.Name),
			"kubernetes"))
	}
	return nil
}
