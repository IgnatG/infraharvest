// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SnsLister lists the region's SNS topics and their subscriptions (the
// `sns` service).
type SnsLister struct {
	Service
}

// List records every topic as an aws_sns_topic, imported by its
// ARN and named after the ARN's last part, each followed by the
// subscriptions Terraform can manage. Any listing error, a topic's
// subscriptions included, fails the service.
func (g *SnsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := sns.NewFromConfig(config)
	topics := sns.NewListTopicsPaginator(svc, &sns.ListTopicsInput{}, stopOnDuplicateToken)
	for topics.HasMorePages() {
		page, err := topics.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, topic := range page.Topics {
			arn := aws.ToString(topic.TopicArn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, snsLastPart(arn), "aws_sns_topic", "aws"))
			if err := g.addSubscriptions(svc, topic.TopicArn); err != nil {
				return err
			}
		}
	}
	return nil
}

// addSubscriptions records the subscriptions of the topic topicARN that
// Terraform can manage as aws_sns_topic_subscription, imported by their
// ARN and named subscription-<the ARN's last part>.
func (g *SnsLister) addSubscriptions(svc *sns.Client, topicARN *string) error {
	pages := sns.NewListSubscriptionsByTopicPaginator(svc, &sns.ListSubscriptionsByTopicInput{TopicArn: topicARN}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subscription := range page.Subscriptions {
			arn := aws.ToString(subscription.SubscriptionArn)
			if !snsManagedSubscription(aws.ToString(subscription.Protocol), arn) {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(arn, "subscription-"+snsLastPart(arn), "aws_sns_topic_subscription", "aws"))
		}
	}
	return nil
}

// snsManagedSubscription reports whether Terraform can manage a
// subscription. It can't import email subscriptions, which the recipient
// confirms outside AWS, nor subscriptions still awaiting confirmation, whose
// ARN is the placeholder "PendingConfirmation".
func snsManagedSubscription(protocol, arn string) bool {
	switch protocol {
	case "email", "email-json":
		return false
	}
	return snsLastPart(arn) != "PendingConfirmation"
}

// snsLastPart returns what follows the last ":" of an SNS ARN: a topic's
// name, or a subscription's ID. A value without ":" is returned whole.
func snsLastPart(arn string) string {
	return arn[strings.LastIndex(arn, ":")+1:]
}
