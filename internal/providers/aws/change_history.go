// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// changeHistoryWindow is how far back CloudTrail's event history goes:
// 90 days of management events, in each region.
const changeHistoryWindow = 90 * 24 * time.Hour

// changeHistoryPages is how many pages of a resource's events, of 50
// each, newest first, LastChanges reads looking for one that changed it:
// a resource read often has many read-only events between its changes.
const changeHistoryPages = 3

// changeHistoryLog names CloudTrail in a change record.
const changeHistoryLog = "CloudTrail"

// LastChanges returns, by each of ids, the newest event CloudTrail's event
// history records that changed the resource of that name (an event that
// isn't read-only), in the region settings names: the region of the
// root's provider block, us-east-1 for the global services, where IAM's
// events are. It asks cloudtrail:LookupEvents, which ReadOnlyAccess
// grants, once a page, for each ID in turn. An ID the history has no
// change of in the last 90 days is left out.
func (p *Provider) LastChanges(ctx context.Context, settings map[string]string, ids []string) (map[string]harvest.ChangeRecord, error) {
	region := settings["region"]
	if region == "" {
		return nil, errors.New("the root's aws provider block names no region, so CloudTrail isn't read")
	}
	in := &Provider{region: region, profile: p.profile, roleARN: p.roleARN, limiter: p.limiter}
	config, err := in.config(ctx)
	if err != nil {
		return nil, err
	}
	client := cloudtrail.NewFromConfig(config)
	start := time.Now().Add(-changeHistoryWindow)
	found := map[string]harvest.ChangeRecord{}
	for _, id := range ids {
		record, ok, err := lastChange(ctx, client, id, start)
		if err != nil {
			return found, fmt.Errorf("cloudtrail:LookupEvents in %s: %w", region, err)
		}
		if ok {
			found[id] = record
		}
	}
	return found, nil
}

// lastChange returns the newest event since start that changed the
// resource named id, reading up to changeHistoryPages pages of its events.
func lastChange(ctx context.Context, client *cloudtrail.Client, id string, start time.Time) (harvest.ChangeRecord, bool, error) {
	pages := cloudtrail.NewLookupEventsPaginator(client, &cloudtrail.LookupEventsInput{
		LookupAttributes: []cloudtrailtypes.LookupAttribute{{
			AttributeKey:   cloudtrailtypes.LookupAttributeKeyResourceName,
			AttributeValue: aws.String(id),
		}},
		StartTime:  aws.Time(start),
		MaxResults: aws.Int32(50),
	}, stopOnDuplicateToken)
	for page := 0; page < changeHistoryPages && pages.HasMorePages(); page++ {
		out, err := pages.NextPage(ctx)
		if err != nil {
			return harvest.ChangeRecord{}, false, err
		}
		for _, e := range out.Events {
			if aws.ToString(e.ReadOnly) == "true" {
				continue
			}
			by := aws.ToString(e.Username)
			if by == "" {
				// An AWS service's own call, such as Auto Scaling's.
				by = aws.ToString(e.EventSource)
			}
			return harvest.ChangeRecord{By: by, Call: aws.ToString(e.EventName), At: aws.ToTime(e.EventTime), Log: changeHistoryLog}, true, nil
		}
	}
	return harvest.ChangeRecord{}, false, nil
}
