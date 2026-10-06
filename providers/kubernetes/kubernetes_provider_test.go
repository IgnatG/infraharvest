// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package kubernetes

import (
	"errors"
	"maps"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

func TestSupportedKinds(t *testing.T) {
	lists := []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "namespaces", Kind: "Namespace", Verbs: []string{"get", "list"}},
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: []string{"list"}},
				{Name: "endpoints", Kind: "Endpoints", Namespaced: true, Verbs: []string{"list"}},
				// No list verb.
				{Name: "bindings", Kind: "Binding", Namespaced: true, Verbs: []string{"create"}},
				// No verbs.
				{Name: "pods/status", Kind: "Pod", Namespaced: true},
				// No Terraform type.
				{Name: "events", Kind: "Event", Namespaced: true, Verbs: []string{"list"}},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: []string{"list"}},
				{Name: "daemonsets", Kind: "DaemonSet", Namespaced: true, Verbs: []string{"list"}},
				{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: []string{"list"}},
			},
		},
		{
			GroupVersion: "apiregistration.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{Name: "apiservices", Kind: "APIService", Verbs: []string{"list"}},
			},
		},
		{GroupVersion: "empty/v1"},
		nil,
	}

	got := supportedKinds(lists)

	want := map[string]Kind{
		"namespaces":  {Name: "Namespace", Resource: "namespaces", Version: "v1"},
		"configmaps":  {Name: "ConfigMap", Resource: "configmaps", Version: "v1", Namespaced: true},
		"endpoints":   {Name: "Endpoints", Resource: "endpoints", Version: "v1", Namespaced: true},
		"deployments": {Name: "Deployment", Resource: "deployments", Group: "apps", Version: "v1", Namespaced: true},
		"apiservices": {Name: "APIService", Resource: "apiservices", Group: "apiregistration.k8s.io", Version: "v1"},
		"daemonsets":  {Name: "DaemonSet", Resource: "daemonsets", Group: "apps", Version: "v1", Namespaced: true},
	}
	gotNames := slices.Sorted(maps.Keys(got))
	wantNames := slices.Sorted(maps.Keys(want))
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("services %v, want %v", gotNames, wantNames)
	}
	for name, w := range want {
		k, ok := got[name].(*Kind)
		if !ok {
			t.Fatalf("%s: %T, want *Kind", name, got[name])
		}
		if k.Name != w.Name || k.Resource != w.Resource || k.Group != w.Group ||
			k.Version != w.Version || k.Namespaced != w.Namespaced {
			t.Errorf("%s: got %s %s %s/%s namespaced=%t, want %s %s %s/%s namespaced=%t", name,
				k.Name, k.Resource, k.Group, k.Version, k.Namespaced,
				w.Name, w.Resource, w.Group, w.Version, w.Namespaced)
		}
	}
}

func TestDiscoveredLists(t *testing.T) {
	lists := []*metav1.APIResourceList{{GroupVersion: "v1"}, {GroupVersion: "apps/v1"}}
	groupsFailed := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("the server is currently unable to handle the request"),
	}}

	tests := []struct {
		name  string
		lists []*metav1.APIResourceList
		err   error
		want  int
	}{
		{name: "complete", lists: lists, want: 2},
		{name: "some groups failed", lists: lists, err: groupsFailed, want: 2},
		{name: "other error with lists", lists: lists, err: errors.New("partial"), want: 2},
		{name: "groups failed, no lists", err: groupsFailed, want: 0},
		{name: "unreachable", err: errors.New("connection refused"), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discoveredLists(tt.lists, tt.err); len(got) != tt.want {
				t.Errorf("got %d lists, want %d", len(got), tt.want)
			}
		})
	}
}
