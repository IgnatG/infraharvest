// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordMediaLiveAPI answers each MediaLive list with one item per page on
// two pages, IDs <kind>-1 and <kind>-2, or with failure for the paths in
// failing.
func recordMediaLiveAPI(t *testing.T, failing ...string) func(apiCall) string {
	return func(call apiCall) string {
		for _, path := range failing {
			if call.Path == path {
				return recordJSONError("ForbiddenException")
			}
		}
		var list, kind string
		switch call.Path {
		case "/prod/channels":
			list, kind = "channels", "channel"
		case "/prod/inputs":
			list, kind = "inputs", "input"
		case "/prod/inputSecurityGroups":
			list, kind = "inputSecurityGroups", "isg"
		default:
			return recordUnexpected(t, call)
		}
		id, token := recordPage(call, kind)
		return fmt.Sprintf(`{%q:[{"id":%q,"arn":"arn:aws:medialive:us-east-1:123456789012:%s:%s"}],"nextToken":%q}`, list, id, kind, id, token)
	}
}

// The lister lists channels, inputs and input security groups by ID, across
// pages.
func TestRecordMediaLiveListsChannelsInputsAndSecurityGroups(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordMediaLiveAPI(t))
	g := &MediaLiveLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_medialive_channel", "channel-1", "channel-1", nil},
		recordedResource{"aws_medialive_channel", "channel-2", "channel-2", nil},
		recordedResource{"aws_medialive_input", "input-1", "input-1", nil},
		recordedResource{"aws_medialive_input", "input-2", "input-2", nil},
		recordedResource{"aws_medialive_input_security_group", "isg-1", "isg-1", nil},
		recordedResource{"aws_medialive_input_security_group", "isg-2", "isg-2", nil},
	)
}

// A list that fails is left out and the others are still listed, and the
// listing reads as partial, naming the lists that failed.
func TestRecordMediaLiveReturnsPartialErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordMediaLiveAPI(t, "/prod/channels", "/prod/inputSecurityGroups"))
	g := &MediaLiveLister{}
	g.SetContext(ctx)

	if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "channels") || !strings.Contains(err.Error(), "input security groups") {
		t.Errorf("want a partial error naming the channels and the input security groups, got %v", err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_medialive_input", "input-1", "input-1", nil},
		recordedResource{"aws_medialive_input", "input-2", "input-2", nil},
	)
}

// When every list fails, so does the service.
func TestRecordMediaLiveReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordMediaLiveAPI(t, "/prod/channels", "/prod/inputs", "/prod/inputSecurityGroups"))
	g := &MediaLiveLister{}
	g.SetContext(ctx)

	err := g.List()
	if err == nil || harvest.IsPartial(err) {
		t.Errorf("want the service failed, got %v", err)
	}
	assertRecordedError(t, err, "ForbiddenException")
}
