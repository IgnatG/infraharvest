// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists instances across pages but those terminated or shutting
// down, named "<id>_<Name tag>", where the Name tag's key is matched in any
// case and the last match wins. It records each instance's user data, read
// with DescribeInstanceAttribute, empty when that fails, and
// source_dest_check "true".
func TestRecordEc2ListsLiveInstances(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		switch call.Op {
		case "DescribeInstances":
			if form.Get("Filter.1.Name") != "" {
				t.Errorf("instances filtered without a filter: %s", call.Body)
			}
			if recordSecondPage(call) {
				return recordEC2(call.Op, `<reservationSet><item><reservationId>r-2</reservationId><instancesSet>
					<item><instanceId>i-3</instanceId><instanceState><code>80</code><name>stopped</name></instanceState>
						<tagSet><item><key>Name</key><value>first</value></item><item><key>team</key><value>data</value></item>
						<item><key>name</key><value>second</value></item></tagSet></item>
					<item><instanceId>i-4</instanceId><instanceState><code>32</code><name>shutting-down</name></instanceState></item>
					<item><instanceId>i-5</instanceId><instanceState><code>0</code><name>pending</name></instanceState></item>
					</instancesSet></item></reservationSet>`)
			}
			return recordEC2(call.Op, `<reservationSet><item><reservationId>r-1</reservationId><instancesSet>
				<item><instanceId>i-1</instanceId><instanceState><code>16</code><name>running</name></instanceState>
					<tagSet><item><key>Name</key><value>web</value></item></tagSet></item>
				<item><instanceId>i-2</instanceId><instanceState><code>48</code><name>terminated</name></instanceState>
					<tagSet><item><key>Name</key><value>gone</value></item></tagSet></item>
				</instancesSet></item></reservationSet><nextToken>`+nextPageToken+`</nextToken>`)
		case "DescribeInstanceAttribute":
			if form.Get("Attribute") != "userData" {
				t.Errorf("instance attribute %q, want userData", form.Get("Attribute"))
			}
			switch id := form.Get("InstanceId"); id {
			case "i-1":
				return recordEC2(call.Op, `<instanceId>i-1</instanceId><userData><value>IyEvYmluL2Jhc2gK</value></userData>`)
			case "i-3":
				return recordEC2Error()
			case "i-5":
				return recordEC2(call.Op, `<instanceId>i-5</instanceId><userData/>`)
			}
		}
		return recordUnexpected(t, call)
	})
	g := &Ec2Lister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_instance", "i-1", "i-1_web",
			map[string]string{"user_data_base64": "IyEvYmluL2Jhc2gK", "source_dest_check": "true"}},
		recordedResource{"aws_instance", "i-3", "i-3_second",
			map[string]string{"user_data_base64": "", "source_dest_check": "true"}},
		recordedResource{"aws_instance", "i-5", "i-5_",
			map[string]string{"user_data_base64": "", "source_dest_check": "true"}},
	)
}

// The tag filters that concern instances, by their type or by the
// service's name, or every service, are passed to DescribeInstances as
// tag:<key> filters; other filters are not.
func TestRecordEc2PassesTagFilters(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeInstances" {
			return recordUnexpected(t, call)
		}
		form, _ := url.ParseQuery(call.Body)
		want := map[string]string{
			"Filter.1.Name":    "tag:env",
			"Filter.1.Value.1": "prod",
			"Filter.2.Name":    "tag:owner",
			"Filter.2.Value.1": "me",
			"Filter.3.Name":    "tag:team",
			"Filter.3.Value.1": "data",
			"Filter.3.Value.2": "ml",
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
		return recordEC2(call.Op, `<reservationSet/>`)
	})
	g := &Ec2Lister{}
	g.SetFilters([]harvest.Filter{
		{Service: "instance", Path: "tags.env", Values: []string{"prod"}},
		{Service: "ebs_volume", Path: "tags.backup", Values: []string{"daily"}},
		// The service's name is taken as the type too.
		{Service: "ec2_instance", Path: "tags.owner", Values: []string{"me"}},
		{Path: "tags.team", Values: []string{"data", "ml"}},
		{Service: "ec2_instance", Path: "id", Values: []string{"i-9"}},
	})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources)
}

func TestRecordEc2ReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &Ec2Lister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
