// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

func recordWorkspacesAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordJSONError("AccessDeniedException")
		}
		switch call.Op {
		case "DescribeWorkspaceDirectories":
			if recordSecondPage(call) {
				return `{"Directories":[{"DirectoryId":"d-3","DirectoryName":"empty.example.com","State":"REGISTERED"},
					{"DirectoryId":"d-old","DirectoryName":"old.example.com","State":"DEREGISTERED"}]}`
			}
			return `{"Directories":[
				{"DirectoryId":"d-1","DirectoryName":"corp.example.com","State":"REGISTERED"},
				{"DirectoryId":"d-2","DirectoryName":"lab.example.com","State":"REGISTERED"}],
				"NextToken":"` + nextPageToken + `"}`
		case "DescribeWorkspaces":
			if recordSecondPage(call) {
				return `{"Workspaces":[{"WorkspaceId":"ws-3","DirectoryId":"d-2","UserName":"carol","State":"STOPPED","BundleId":"wsb-1"},
					{"WorkspaceId":"ws-4","DirectoryId":"d-2","UserName":"dave","State":"TERMINATED","BundleId":"wsb-1"}]}`
			}
			return `{"Workspaces":[
				{"WorkspaceId":"ws-1","DirectoryId":"d-1","UserName":"alice","State":"AVAILABLE","BundleId":"wsb-1"},
				{"WorkspaceId":"ws-2","DirectoryId":"d-1","UserName":"bob","State":"AVAILABLE","BundleId":"wsb-1"}],
				"NextToken":"` + nextPageToken + `"}`
		case "DescribeIpGroups":
			if recordSecondPage(call) {
				return `{"Result":[{"groupId":"wsipg-2","groupName":"vpn"}]}`
			}
			return `{"Result":[{"groupId":"wsipg-1","groupName":"office","userRules":[{"ipRule":"10.0.0.0/8"}]}],"NextToken":"` + nextPageToken + `"}`
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists the registered directories, each once, with workspaces
// or not, then the workspaces, then the IP access control groups, by ID
// and across pages. Deregistered directories and terminated workspaces are
// left out.
func TestRecordWorkspacesListsDirectoriesWorkspacesAndIPGroups(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordWorkspacesAPI(t, ""))
	g := &WorkspacesLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_workspaces_directory", "d-1", "d-1", nil},
		recordedResource{"aws_workspaces_directory", "d-2", "d-2", nil},
		recordedResource{"aws_workspaces_directory", "d-3", "d-3", nil},
		recordedResource{"aws_workspaces_workspace", "ws-1", "ws-1", nil},
		recordedResource{"aws_workspaces_workspace", "ws-2", "ws-2", nil},
		recordedResource{"aws_workspaces_workspace", "ws-3", "ws-3", nil},
		recordedResource{"aws_workspaces_ip_group", "wsipg-1", "wsipg-1", nil},
		recordedResource{"aws_workspaces_ip_group", "wsipg-2", "wsipg-2", nil},
	)
}

func TestRecordWorkspacesReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"DescribeWorkspaceDirectories", "DescribeWorkspaces", "DescribeIpGroups"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordWorkspacesAPI(t, failing))
			g := &WorkspacesLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}
