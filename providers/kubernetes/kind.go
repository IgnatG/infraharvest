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
	"context"
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// listPageSize is the number of objects asked for per List call, as kubectl does.
const listPageSize = 500

// Kind lists the objects of one API resource, as discovery names it.
type Kind struct {
	KubernetesService
	// Name is the kind, e.g. "Deployment".
	Name string
	// Resource is the plural resource name, e.g. "deployments".
	Resource   string
	Group      string
	Version    string
	Namespaced bool
}

// GroupVersionResource returns the resource the dynamic client lists.
func (k *Kind) GroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
}

// InitResources lists every object of the kind, one Terraform resource each.
func (k *Kind) InitResources() error {
	config, _, err := initClientAndConfig()
	if err != nil {
		return err
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	return k.listResources(k.Context(), client)
}

// listResources lists the kind in all namespaces, page by page, and records
// the objects no other object owns. The import ID is "namespace/name" for a
// namespaced kind and "name" for a cluster-scoped one.
func (k *Kind) listResources(ctx context.Context, client dynamic.Interface) error {
	resource := client.Resource(k.GroupVersionResource())
	tfType, ok := terraformType(k.Name)
	if !ok {
		return fmt.Errorf("kubernetes: %s has no Terraform resource type", k.Name)
	}

	opts := metav1.ListOptions{Limit: listPageSize}
	for {
		list, err := resource.List(ctx, opts)
		if err != nil {
			return err
		}

		for i := range list.Items {
			item := &list.Items[i]
			// Owned objects (pods of a ReplicaSet, ...) come with their owner.
			if len(item.GetOwnerReferences()) > 0 {
				continue
			}

			id := item.GetName()
			if k.Namespaced {
				id = item.GetNamespace() + "/" + item.GetName()
			}

			k.Resources = append(k.Resources, terraformutils.NewSimpleResource(
				id,
				id,
				tfType,
				"kubernetes"))
		}

		opts.Continue = list.GetContinue()
		if opts.Continue == "" {
			return nil
		}
	}
}
