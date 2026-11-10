// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// sqsPageSize is how many queue URLs one ListQueues call asks for. Without a
// page size, ListQueues answers with at most 1000 queues and no NextToken,
// so the rest could never be listed.
const sqsPageSize = 1000

// SqsLister lists the region's SQS queues (the `sqs` service).
type SqsLister struct {
	Service
}

// List records every queue as an aws_sqs_queue, imported by its
// URL and named after the URL's last part, the queue name. When the
// environment variable INFRAHARVEST_SQS_PREFIX (or SQS_PREFIX, its old
// name) is set, even to "", it is passed on as the queue name prefix (see
// sqsPrefix). Any listing error fails the service.
func (g *SqsLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *SqsLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	input := &sqs.ListQueuesInput{MaxResults: aws.Int32(sqsPageSize)}
	if prefix, ok := sqsPrefix(); ok {
		input.QueueNamePrefix = aws.String(prefix)
	}
	pages := sqs.NewListQueuesPaginator(sqs.NewFromConfig(config), input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, queueURL := range page.QueueUrls {
			name := queueURL[strings.LastIndex(queueURL, "/")+1:]
			g.Resources = append(g.Resources, harvest.RecordBare(queueURL, name, "aws_sqs_queue", "aws"))
		}
	}
	return nil
}

// sqsPrefixEnv is the environment variable whose value, when it is set,
// is the prefix the sqs service lists the queues of; sqsPrefixOldEnv is
// its name before every variable of infraharvest's took the prefix
// INFRAHARVEST_, still read when the new one isn't set.
const (
	sqsPrefixEnv    = "INFRAHARVEST_SQS_PREFIX"
	sqsPrefixOldEnv = "SQS_PREFIX"
)

// sqsPrefix returns the queue name prefix the environment sets, and
// whether it sets one.
func sqsPrefix() (string, bool) {
	if prefix, ok := os.LookupEnv(sqsPrefixEnv); ok {
		return prefix, true
	}
	return os.LookupEnv(sqsPrefixOldEnv)
}
