// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordEfsAPI answers EFS with two pages of file systems: fs-1 with two
// mount targets and a policy, fs-2 whose mount targets cannot be read, and
// fs-3 with neither mount targets nor a policy; and two pages of access
// points. The paths in failing answer with an error.
func recordEfsAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if failing != "" && call.Path == failing {
			return recordJSONError("AccessDeniedException")
		}
		switch call.Path {
		case "/2015-02-01/file-systems":
			if recordSecondPage(call) {
				return `{"FileSystems":[
					{"FileSystemId":"fs-2","Name":"scratch","LifeCycleState":"available","NumberOfMountTargets":1},
					{"FileSystemId":"fs-3","LifeCycleState":"available","NumberOfMountTargets":0}]}`
			}
			return fmt.Sprintf(`{"FileSystems":[{"FileSystemId":"fs-1","Name":"shared","LifeCycleState":"available","NumberOfMountTargets":2}],
				"NextMarker":%q}`, nextPageToken)
		case "/2015-02-01/mount-targets":
			switch call.Query.Get("FileSystemId") {
			case "fs-1":
				return `{"MountTargets":[
					{"MountTargetId":"fsmt-1a","FileSystemId":"fs-1","SubnetId":"subnet-1","LifeCycleState":"available"},
					{"MountTargetId":"fsmt-1b","FileSystemId":"fs-1","SubnetId":"subnet-2","LifeCycleState":"available"}]}`
			case "fs-2":
				return recordJSONError("AccessDeniedException")
			case "fs-3":
				return `{"MountTargets":[]}`
			}
		case "/2015-02-01/file-systems/fs-1/policy":
			return `{"FileSystemId":"fs-1","Policy":"{\"Version\":\"2012-10-17\",\"Statement\":[]}"}`
		case "/2015-02-01/file-systems/fs-3/policy":
			return `{"__type":"PolicyNotFound","ErrorCode":"PolicyNotFound","Message":"No policy found"}`
		case "/2015-02-01/access-points":
			if recordSecondPage(call) {
				return `{"AccessPoints":[{"AccessPointId":"fsap-2","FileSystemId":"fs-3","LifeCycleState":"available"}]}`
			}
			return fmt.Sprintf(`{"AccessPoints":[{"AccessPointId":"fsap-1","FileSystemId":"fs-1","Name":"app","LifeCycleState":"available"}],
				"NextToken":%q}`, nextPageToken)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists file systems across pages, each followed by its mount
// targets and its policy, then access points across pages, all by ID. A
// file system whose mount targets cannot be read is listed without its
// mount targets and without its policy, which is not asked for, and the
// listing reads as partial; one without a policy is listed without.
func TestRecordEfsListsFileSystemsMountTargetsPoliciesAndAccessPoints(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordEfsAPI(t, ""))
	g := &EfsLister{}
	g.SetContext(ctx)

	if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "mount targets of fs-2") {
		t.Errorf("want a partial error for the mount targets of fs-2, got %v", err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_efs_file_system", "fs-1", "fs-1", nil},
		recordedResource{"aws_efs_mount_target", "fsmt-1a", "fsmt-1a", nil},
		recordedResource{"aws_efs_mount_target", "fsmt-1b", "fsmt-1b", nil},
		recordedResource{"aws_efs_file_system_policy", "fs-1", "fs-1", map[string]string{
			"file_system_id": "fs-1",
			"policy":         `{"Version":"2012-10-17","Statement":[]}`,
		}},
		recordedResource{"aws_efs_file_system", "fs-2", "fs-2", nil},
		recordedResource{"aws_efs_file_system", "fs-3", "fs-3", nil},
		recordedResource{"aws_efs_access_point", "fsap-1", "fsap-1", nil},
		recordedResource{"aws_efs_access_point", "fsap-2", "fsap-2", nil},
	)
}

// Listing file systems or access points fails the service.
func TestRecordEfsReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"/2015-02-01/file-systems", "/2015-02-01/access-points"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordEfsAPI(t, failing))
			g := &EfsLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}
