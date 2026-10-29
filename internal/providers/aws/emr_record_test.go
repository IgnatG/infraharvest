// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists the clusters starting, running or waiting by ID, named
// after their name, then security configurations by name, each across
// pages. It asks for those states, and leaves out a cluster of another
// state that the answer has all the same.
func TestRecordEmrListsClustersAndSecurityConfigurations(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListClusters":
			if !strings.Contains(call.Body, `"ClusterStates":["STARTING","BOOTSTRAPPING","RUNNING","WAITING"]`) {
				t.Errorf("clusters listed in every state: %s", call.Body)
			}
			if recordSecondPage(call) {
				return `{"Clusters":[{"Id":"j-2OLD","Name":"backfill","Status":{"State":"TERMINATED"}},{"Id":"j-3NEW","Name":"adhoc","Status":{"State":"STARTING"}}]}`
			}
			return fmt.Sprintf(`{"Clusters":[{"Id":"j-1ETL","Name":"nightly etl","Status":{"State":"WAITING"},"NormalizedInstanceHours":24}],
				"Marker":%q}`, nextPageToken)
		case "ListSecurityConfigurations":
			if recordSecondPage(call) {
				return `{"SecurityConfigurations":[{"Name":"tls-only"}]}`
			}
			return fmt.Sprintf(`{"SecurityConfigurations":[{"Name":"kms-at-rest"}],"Marker":%q}`, nextPageToken)
		}
		return recordUnexpected(t, call)
	})
	g := &EmrLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_emr_cluster", "j-1ETL", "nightly etl", nil},
		recordedResource{"aws_emr_cluster", "j-3NEW", "adhoc", nil},
		recordedResource{"aws_emr_security_configuration", "kms-at-rest", "kms-at-rest", nil},
		recordedResource{"aws_emr_security_configuration", "tls-only", "tls-only", nil},
	)
}

// Each list fails the service.
func TestRecordEmrReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListClusters", "ListSecurityConfigurations"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("InvalidRequestException")
				case "ListClusters":
					return `{"Clusters":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &EmrLister{}

			assertRecordedError(t, listError(ctx, g), "InvalidRequestException")
		})
	}
}
