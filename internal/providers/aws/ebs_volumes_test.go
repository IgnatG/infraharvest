// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists volumes across pages, each followed by its attachments
// in state attached, but leaves out a volume that is an instance's root
// device, with its attachments, for the instance to manage. The attached
// instances are described in one DescribeInstances call, by an instance-id
// filter, however many pages and volumes name them. An attachment is
// recorded by the ID Terraform derives from its device, instance and
// volume (its import ID is pinned by TestImportID), named
// "<instance>:<device>". The DescribeInstances error is pinned by
// TestEbsReturnsDescribeInstancesError.
func TestRecordEbsListsVolumesAndAttachments(t *testing.T) {
	t.Parallel()
	describes := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		switch call.Op {
		case "DescribeVolumes":
			if form.Get("Filter.1.Name") != "" {
				t.Errorf("volumes filtered without a filter: %s", call.Body)
			}
			if recordSecondPage(call) {
				return recordEC2(call.Op, `<volumeSet>
					<item><volumeId>vol-3</volumeId><size>50</size><status>in-use</status>
						<attachmentSet><item><volumeId>vol-3</volumeId><instanceId>i-2</instanceId><device>/dev/sdg</device><status>attaching</status></item></attachmentSet></item>
					<item><volumeId>vol-4</volumeId><size>10</size><status>available</status><attachmentSet/></item>
					</volumeSet>`)
			}
			return recordEC2(call.Op, `<volumeSet>
				<item><volumeId>vol-1</volumeId><size>100</size><status>in-use</status>
					<attachmentSet><item><volumeId>vol-1</volumeId><instanceId>i-1</instanceId><device>/dev/sdf</device><status>attached</status></item></attachmentSet></item>
				<item><volumeId>vol-root</volumeId><size>8</size><status>in-use</status>
					<attachmentSet><item><volumeId>vol-root</volumeId><instanceId>i-1</instanceId><device>/dev/xvda</device><status>attached</status></item></attachmentSet></item>
				</volumeSet><nextToken>`+nextPageToken+`</nextToken>`)
		case "DescribeInstances":
			describes++
			if form.Get("InstanceId.1") != "" {
				t.Errorf("instances named by InstanceIds, which fails on a gone instance: %s", call.Body)
			}
			if form.Get("Filter.1.Name") != "instance-id" || form.Get("Filter.1.Value.1") != "i-1" ||
				form.Get("Filter.1.Value.2") != "i-2" || form.Get("Filter.1.Value.3") != "" {
				t.Errorf("instances described by %s, want an instance-id filter on i-1 and i-2", call.Body)
			}
			return recordEC2(call.Op, `<reservationSet>
				<item><reservationId>r-1</reservationId><instancesSet><item><instanceId>i-1</instanceId><rootDeviceName>/dev/xvda</rootDeviceName></item></instancesSet></item>
				<item><reservationId>r-2</reservationId><instancesSet><item><instanceId>i-2</instanceId><rootDeviceName>/dev/xvda</rootDeviceName></item></instancesSet></item>
				</reservationSet>`)
		}
		return recordUnexpected(t, call)
	})
	g := &EbsLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	if describes != 1 {
		t.Errorf("DescribeInstances called %d times, want once", describes)
	}
	assertRecorded(t, resources,
		recordedResource{"aws_ebs_volume", "vol-1", "vol-1", nil},
		// vai- and the CRC-32 of "/dev/sdf-i-1-vol-1-".
		recordedResource{"aws_volume_attachment", "vai-3944550031", "i-1:/dev/sdf",
			map[string]string{"device_name": "/dev/sdf", "volume_id": "vol-1", "instance_id": "i-1"}},
		recordedResource{"aws_ebs_volume", "vol-3", "vol-3", nil},
		recordedResource{"aws_ebs_volume", "vol-4", "vol-4", nil},
	)
}

// An instance that is gone by the time it is described is missing from
// the DescribeInstances answer rather than failing it; its volume is not
// taken for a root volume and the listing goes on.
func TestRecordEbsKeepsVolumesOfGoneInstances(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeVolumes":
			return recordEC2(call.Op, `<volumeSet>
				<item><volumeId>vol-1</volumeId><status>in-use</status>
					<attachmentSet><item><volumeId>vol-1</volumeId><instanceId>i-gone</instanceId><device>/dev/xvda</device><status>attached</status></item></attachmentSet></item>
				</volumeSet>`)
		case "DescribeInstances":
			return recordEC2(call.Op, `<reservationSet/>`)
		}
		return recordUnexpected(t, call)
	})
	g := &EbsLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_ebs_volume", "vol-1", "vol-1", nil},
		recordedResource{"aws_volume_attachment", volumeAttachmentID("/dev/xvda", "vol-1", "i-gone"), "i-gone:/dev/xvda",
			map[string]string{"device_name": "/dev/xvda", "volume_id": "vol-1", "instance_id": "i-gone"}},
	)
}

// Volumes attached to no instance need no DescribeInstances call.
func TestRecordEbsDescribesNoInstancesForDetachedVolumes(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeVolumes" {
			return recordUnexpected(t, call)
		}
		return recordEC2(call.Op, `<volumeSet><item><volumeId>vol-1</volumeId><status>available</status><attachmentSet/></item></volumeSet>`)
	})
	g := &EbsLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources, recordedResource{"aws_ebs_volume", "vol-1", "vol-1", nil})
}

// The tag filters that concern volumes, or every service, are passed to
// DescribeVolumes as tag:<key> filters; other filters are not.
func TestRecordEbsPassesTagFilters(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeVolumes" {
			return recordUnexpected(t, call)
		}
		form, _ := url.ParseQuery(call.Body)
		want := map[string]string{
			"Filter.1.Name":    "tag:env",
			"Filter.1.Value.1": "prod",
			"Filter.1.Value.2": "dev",
			"Filter.2.Name":    "tag:team",
			"Filter.2.Value.1": "data",
		}
		for key := range form {
			if _, ok := want[key]; strings.HasPrefix(key, "Filter.") && !ok {
				t.Errorf("unwanted filter %s=%q", key, form.Get(key))
			}
		}
		for key, value := range want {
			if form.Get(key) != value {
				t.Errorf("filter %s=%q, want %q", key, form.Get(key), value)
			}
		}
		return recordEC2(call.Op, `<volumeSet/>`)
	})
	g := &EbsLister{}
	filters := []harvest.Filter{
		{Service: "ebs_volume", Path: "tags.env", Values: []string{"prod", "dev"}},
		{Service: "instance", Path: "tags.role", Values: []string{"web"}},
		{Path: "tags.team", Values: []string{"data"}},
		{Service: "ebs_volume", Path: "id", Values: []string{"vol-9"}},
	}

	resources, err := listChecked(ctx, t, g, harvest.Settings{Filters: filters})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources)
}

func TestRecordEbsReturnsDescribeVolumesErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &EbsLister{}

	assertRecordedError(t, listError(ctx, g), "UnauthorizedOperation")
}
