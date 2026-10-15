// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// LogsLister lists the region's CloudWatch Logs log groups (the `logs`
// service).
type LogsLister struct {
	Service
}

// List records every log group as an aws_cloudwatch_log_group,
// imported by its name, with its retention in days and its KMS key when it
// has them. The groups Lambda creates for its functions are listed too: the
// catalogue's skip rules leave them out later. Any listing error fails the
// service.
func (g *LogsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := cloudwatchlogs.NewDescribeLogGroupsPaginator(cloudwatchlogs.NewFromConfig(config), &cloudwatchlogs.DescribeLogGroupsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.LogGroups {
			attributes := map[string]string{}
			if group.RetentionInDays != nil {
				attributes["retention_in_days"] = strconv.Itoa(int(*group.RetentionInDays))
			}
			if group.KmsKeyId != nil {
				attributes["kms_key_id"] = *group.KmsKeyId
			}
			name := aws.ToString(group.LogGroupName)
			g.Resources = append(g.Resources, harvest.Record(name, name, "aws_cloudwatch_log_group", "aws", attributes))
		}
	}
	return nil
}
