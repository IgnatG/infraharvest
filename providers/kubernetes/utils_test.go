// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package kubernetes

import (
	"regexp"
	"testing"
)

// kindTypes maps each built-in kind to the Terraform type it imports as, or
// "" when the Terraform kubernetes provider has no type for it.
var kindTypes = map[string]string{
	// core/v1
	"ConfigMap":             "kubernetes_config_map",
	"Endpoints":             "kubernetes_endpoints",
	"LimitRange":            "kubernetes_limit_range",
	"Namespace":             "kubernetes_namespace",
	"PersistentVolume":      "kubernetes_persistent_volume",
	"PersistentVolumeClaim": "kubernetes_persistent_volume_claim",
	"Pod":                   "kubernetes_pod",
	"ReplicationController": "kubernetes_replication_controller",
	"ResourceQuota":         "kubernetes_resource_quota",
	"Secret":                "kubernetes_secret",
	"Service":               "kubernetes_service",
	"ServiceAccount":        "kubernetes_service_account",
	"Event":                 "",
	"Node":                  "",
	"PodTemplate":           "",
	// apps/v1
	"DaemonSet":          "kubernetes_daemon_set_v1",
	"Deployment":         "kubernetes_deployment",
	"StatefulSet":        "kubernetes_stateful_set",
	"ReplicaSet":         "",
	"ControllerRevision": "",
	// batch/v1
	"CronJob": "kubernetes_cron_job",
	"Job":     "kubernetes_job",
	// autoscaling
	"HorizontalPodAutoscaler": "kubernetes_horizontal_pod_autoscaler",
	// networking.k8s.io/v1, discovery.k8s.io/v1
	"Ingress":       "kubernetes_ingress",
	"IngressClass":  "kubernetes_ingress_class",
	"NetworkPolicy": "kubernetes_network_policy",
	"EndpointSlice": "kubernetes_endpoint_slice_v1",
	// policy
	"PodDisruptionBudget": "kubernetes_pod_disruption_budget",
	"PodSecurityPolicy":   "kubernetes_pod_security_policy",
	// rbac.authorization.k8s.io/v1
	"ClusterRole":        "kubernetes_cluster_role",
	"ClusterRoleBinding": "kubernetes_cluster_role_binding",
	"Role":               "kubernetes_role",
	"RoleBinding":        "kubernetes_role_binding",
	// storage.k8s.io/v1
	"CSIDriver":        "kubernetes_csi_driver",
	"StorageClass":     "kubernetes_storage_class",
	"CSINode":          "",
	"VolumeAttachment": "",
	// scheduling.k8s.io/v1, node.k8s.io/v1
	"PriorityClass": "kubernetes_priority_class",
	"RuntimeClass":  "kubernetes_runtime_class_v1",
	// admissionregistration.k8s.io/v1
	"MutatingWebhookConfiguration":     "kubernetes_mutating_webhook_configuration",
	"ValidatingWebhookConfiguration":   "kubernetes_validating_webhook_configuration",
	"ValidatingAdmissionPolicy":        "kubernetes_validating_admission_policy",
	"ValidatingAdmissionPolicyBinding": "",
	// apiregistration.k8s.io/v1, certificates.k8s.io/v1, authentication.k8s.io/v1
	"APIService":                "kubernetes_api_service",
	"CertificateSigningRequest": "kubernetes_certificate_signing_request",
	"TokenRequest":              "kubernetes_token_request_v1",
	// apiextensions.k8s.io/v1, coordination.k8s.io/v1
	"CustomResourceDefinition": "",
	"Lease":                    "",
}

func TestTerraformType(t *testing.T) {
	for kind, want := range kindTypes {
		got, ok := terraformType(kind)
		if got != want || ok != (want != "") {
			t.Errorf("terraformType(%q) = %q, %t, want %q", kind, got, ok, want)
		}
		if _, exists := supportedResourceTypes[got]; ok && !exists {
			t.Errorf("terraformType(%q) = %q, not a provider type", kind, got)
		}
	}
}

// notKinds are provider types that manage part of an object or something
// other than an API object, so no kind lists as them.
var notKinds = map[string]bool{
	"kubernetes_annotations":                true,
	"kubernetes_config_map_v1_data":         true,
	"kubernetes_default_service_account":    true,
	"kubernetes_default_service_account_v1": true,
	"kubernetes_env":                        true,
	"kubernetes_labels":                     true,
	"kubernetes_manifest":                   true,
	"kubernetes_node_taint":                 true,
	"kubernetes_secret_v1_data":             true,
}

var versionSuffix = regexp.MustCompile(`_v\d+((alpha|beta)\d+)?$`)

// TestEveryTypeHasAKind checks the table above covers the provider: each
// type is what a kind maps to, a versioned alias of one (kubernetes_pod_v1 of
// kubernetes_pod) or one of notKinds.
func TestEveryTypeHasAKind(t *testing.T) {
	mapped := map[string]bool{}
	for _, tfType := range kindTypes {
		if tfType != "" {
			mapped[tfType] = true
		}
	}
	for tfType := range supportedResourceTypes {
		if mapped[tfType] || notKinds[tfType] || mapped[versionSuffix.ReplaceAllString(tfType, "")] {
			continue
		}
		if tfType == "kubernetes_daemonset" { // the old name of kubernetes_daemon_set_v1
			continue
		}
		t.Errorf("%s: no kind in kindTypes maps to it", tfType)
	}
}
