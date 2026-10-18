// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/firehose"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// firehosePageSize is how many delivery streams one ListDeliveryStreams call
// asks for, the most the API allows.
const firehosePageSize = 100

// FirehoseLister lists the region's Amazon Data Firehose delivery streams
// (the `firehose` service).
type FirehoseLister struct {
	Service
}

// List records every delivery stream as an
// aws_kinesis_firehose_delivery_stream by its ARN, which is what Terraform
// imports it by, named after its name, which it also records as the name
// attribute. ListDeliveryStreams answers with names only, and a stream's
// ARN is made of its partition, region, account and name, so the ARN is
// built from the name rather than described for, at one account lookup
// for the whole listing (see callerAccount). Any error fails the service.
func (g *FirehoseLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := firehose.NewFromConfig(config)
	var account string
	// The API has no paginator: each page starts after the last stream of
	// the page before, for as long as the answer says more streams follow.
	return paginateByMarker(func(after *string) (*string, error) {
		page, err := svc.ListDeliveryStreams(g.Context(), &firehose.ListDeliveryStreamsInput{
			Limit:                            aws.Int32(firehosePageSize),
			ExclusiveStartDeliveryStreamName: after,
		})
		if err != nil {
			return nil, err
		}
		names := page.DeliveryStreamNames
		if len(names) > 0 && account == "" {
			id, err := g.callerAccount(config)
			if err != nil {
				return nil, fmt.Errorf("find the AWS account, for the delivery streams' ARNs: %w", err)
			}
			account = aws.ToString(id)
		}
		for _, name := range names {
			arn := "arn:" + partitionOf(config.Region) + ":firehose:" + config.Region + ":" + account + ":deliverystream/" + name
			g.Resources = append(g.Resources, harvest.Record(arn, name,
				"aws_kinesis_firehose_delivery_stream", "aws", map[string]string{"name": name}))
		}
		// A page that claims more streams but names none leaves nothing to
		// continue from, so the listing ends there.
		if !aws.ToBool(page.HasMoreDeliveryStreams) || len(names) == 0 {
			return nil, nil
		}
		return aws.String(names[len(names)-1]), nil
	})
}

// regionPartitions are the partitions whose regions' names start with
// something of their own; every other region is in the aws partition.
var regionPartitions = []struct{ prefix, partition string }{
	{"cn-", "aws-cn"},
	{"us-gov-", "aws-us-gov"},
	{"us-isob-", "aws-iso-b"},
	{"us-iso-", "aws-iso"},
	{"eu-isoe-", "aws-iso-e"},
	{"us-isof-", "aws-iso-f"},
	{"eusc-", "aws-eusc"},
}

// partitionOf returns the partition of region, as ARNs name it.
func partitionOf(region string) string {
	for _, p := range regionPartitions {
		if strings.HasPrefix(region, p.prefix) {
			return p.partition
		}
	}
	return "aws"
}
