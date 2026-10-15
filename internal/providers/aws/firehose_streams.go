// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/firehose"
	"github.com/aws/aws-sdk-go-v2/service/firehose/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
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
// attribute. ListDeliveryStreams answers with names only, so each stream
// is described for its ARN. A listing error fails the service; a stream
// whose description fails, such as one deleted since it was listed, is
// logged and left out, and so is a stream being deleted.
func (g *FirehoseLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := firehose.NewFromConfig(config)
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
		for _, name := range names {
			g.addDeliveryStream(svc, name)
		}
		// A page that claims more streams but names none leaves nothing to
		// continue from, so the listing ends there.
		if !aws.ToBool(page.HasMoreDeliveryStreams) || len(names) == 0 {
			return nil, nil
		}
		return aws.String(names[len(names)-1]), nil
	})
}

// addDeliveryStream describes the delivery stream name and records it by
// its ARN, unless the description fails or the stream is being deleted.
func (g *FirehoseLister) addDeliveryStream(svc *firehose.Client, name string) {
	out, err := svc.DescribeDeliveryStream(g.Context(), &firehose.DescribeDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
	})
	if err != nil {
		log.Printf("aws: firehose: can't describe delivery stream %s; leaving it out: %v", name, err)
		return
	}
	stream := out.DeliveryStreamDescription
	if stream == nil || stream.DeliveryStreamStatus == types.DeliveryStreamStatusDeleting {
		return
	}
	g.Resources = append(g.Resources, harvest.Record(aws.ToString(stream.DeliveryStreamARN), name,
		"aws_kinesis_firehose_delivery_stream", "aws", map[string]string{"name": name}))
}
