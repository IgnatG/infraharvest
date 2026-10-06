// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package kubernetes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func object(apiVersion, kind, namespace, name string, owned bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(namespace)
	u.SetName(name)
	if owned {
		controller := true
		u.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "ReplicaSet",
			Name:       "owner",
			UID:        "1",
			Controller: &controller,
		}})
	}
	return u
}

// listed returns the import ID, name and type of each resource k recorded,
// sorted by import ID.
func listed(k *Kind) [][3]string {
	var got [][3]string
	for _, r := range k.Resources {
		got = append(got, [3]string{r.InstanceState.ID, r.RawName, r.InstanceInfo.Type})
	}
	slices.SortFunc(got, func(a, b [3]string) int { return strings.Compare(a[0], b[0]) })
	return got
}

func TestListResources(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		object("apps/v1", "Deployment", "default", "web", false),
		object("apps/v1", "Deployment", "kube-system", "coredns", false),
		object("apps/v1", "Deployment", "default", "owned", true),
		object("v1", "Namespace", "", "default", false),
		object("v1", "Namespace", "", "team-a", false),
		object("v1", "Namespace", "", "owned-ns", true),
		object("v1", "Pod", "default", "web-123", true),
		object("apps/v1", "DaemonSet", "kube-system", "kube-proxy", false),
	)

	tests := []struct {
		name string
		kind Kind
		want [][3]string
	}{
		{
			name: "namespaced",
			kind: Kind{Name: "Deployment", Resource: "deployments", Group: "apps", Version: "v1", Namespaced: true},
			want: [][3]string{
				{"default/web", "default/web", "kubernetes_deployment"},
				{"kube-system/coredns", "kube-system/coredns", "kubernetes_deployment"},
			},
		},
		{
			name: "cluster-scoped",
			kind: Kind{Name: "Namespace", Resource: "namespaces", Version: "v1"},
			want: [][3]string{
				{"default", "default", "kubernetes_namespace"},
				{"team-a", "team-a", "kubernetes_namespace"},
			},
		},
		{
			name: "versioned type",
			kind: Kind{Name: "DaemonSet", Resource: "daemonsets", Group: "apps", Version: "v1", Namespaced: true},
			want: [][3]string{
				{"kube-system/kube-proxy", "kube-system/kube-proxy", "kubernetes_daemon_set_v1"},
			},
		},
		{
			name: "all owned",
			kind: Kind{Name: "Pod", Resource: "pods", Version: "v1", Namespaced: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := tt.kind
			if err := k.listResources(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			if got := listed(&k); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			for _, r := range k.Resources {
				if r.Provider != "kubernetes" || len(r.InstanceState.Attributes) != 0 {
					t.Errorf("%s: provider %q, attributes %v", r.InstanceState.ID, r.Provider, r.InstanceState.Attributes)
				}
			}
		})
	}
}

// pagedClient serves one resource in pages, keyed by continue token, and
// records the options of each List call.
type pagedClient struct {
	dynamic.Interface
	gvr   schema.GroupVersionResource
	pages map[string]*unstructured.UnstructuredList
	calls []metav1.ListOptions
}

type pagedResource struct {
	dynamic.NamespaceableResourceInterface
	client *pagedClient
}

func (c *pagedClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	if gvr != c.gvr {
		panic(fmt.Sprintf("unexpected resource %v", gvr))
	}
	return &pagedResource{client: c}
}

func (r *pagedResource) List(_ context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	r.client.calls = append(r.client.calls, opts)
	page, ok := r.client.pages[opts.Continue]
	if !ok {
		return nil, fmt.Errorf("unknown continue token %q", opts.Continue)
	}
	return page, nil
}

func page(next string, items ...*unstructured.Unstructured) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetContinue(next)
	for _, item := range items {
		list.Items = append(list.Items, *item)
	}
	return list
}

func TestListResourcesPaginates(t *testing.T) {
	client := &pagedClient{
		gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"},
		pages: map[string]*unstructured.UnstructuredList{
			"": page("p2",
				object("v1", "ConfigMap", "a", "one", false),
				object("v1", "ConfigMap", "a", "owned", true)),
			"p2": page("p3", object("v1", "ConfigMap", "b", "two", false)),
			"p3": page("", object("v1", "ConfigMap", "c", "three", false)),
		},
	}
	k := Kind{Name: "ConfigMap", Resource: "configmaps", Version: "v1", Namespaced: true}
	if err := k.listResources(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	want := [][3]string{
		{"a/one", "a/one", "kubernetes_config_map"},
		{"b/two", "b/two", "kubernetes_config_map"},
		{"c/three", "c/three", "kubernetes_config_map"},
	}
	if got := listed(&k); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	var tokens []string
	for _, call := range client.calls {
		if call.Limit != listPageSize {
			t.Errorf("Limit = %d, want %d", call.Limit, listPageSize)
		}
		tokens = append(tokens, call.Continue)
	}
	if want := []string{"", "p2", "p3"}; !slices.Equal(tokens, want) {
		t.Errorf("continue tokens %q, want %q", tokens, want)
	}
}

func TestListResourcesError(t *testing.T) {
	client := &pagedClient{
		gvr:   schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		pages: map[string]*unstructured.UnstructuredList{"": page("expired")},
	}
	k := Kind{Name: "Secret", Resource: "secrets", Version: "v1", Namespaced: true}
	if err := k.listResources(context.Background(), client); err == nil {
		t.Error("want the error of the second page")
	}
}

func TestListResourcesNoType(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	k := Kind{Name: "Event", Resource: "events", Version: "v1", Namespaced: true}
	if err := k.listResources(context.Background(), client); err == nil {
		t.Error("want an error for a kind without a Terraform type")
	}
}
