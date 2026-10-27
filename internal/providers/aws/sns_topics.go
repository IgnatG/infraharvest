// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
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
// subscriptions Terraform can manage. The subscriptions of a page's
// topics are read resourceParallel topics at a time, and recorded in the
// page's order. They are asked for by topic, not account-wide with
// ListSubscriptions, which leaves out what other accounts subscribed to
// the account's topics. Any listing error, a topic's subscriptions
// included, fails the service, with what was read up to it recorded.
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
		reads := make([][]harvest.Resource, len(page.Topics))
		errs := make([]error, len(page.Topics))
		inParallel(len(page.Topics), resourceParallel, func(i int) {
			reads[i], errs[i] = g.topic(svc, page.Topics[i].TopicArn)
		})
		for i := range page.Topics {
			g.Resources = append(g.Resources, reads[i]...)
			if errs[i] != nil {
				return errs[i]
			}
		}
	}
	return nil
}

// topic returns the topic topicARN, followed by its subscriptions that
// Terraform can manage as aws_sns_topic_subscription, imported by their
// ARN and named after their endpoint and protocol (see subscriptionName).
// An error is returned with what was read before it.
func (g *SnsLister) topic(svc *sns.Client, topicARN *string) ([]harvest.Resource, error) {
	arn := aws.ToString(topicARN)
	resources := []harvest.Resource{harvest.RecordBare(arn, snsLastPart(arn), "aws_sns_topic", "aws")}
	pages := sns.NewListSubscriptionsByTopicPaginator(svc, &sns.ListSubscriptionsByTopicInput{TopicArn: topicARN}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return resources, err
		}
		for _, subscription := range page.Subscriptions {
			arn := aws.ToString(subscription.SubscriptionArn)
			if !snsManagedSubscription(aws.ToString(subscription.Protocol), arn) {
				continue
			}
			resources = append(resources, harvest.RecordBare(arn, subscriptionName(subscription.Endpoint, subscription.Protocol), "aws_sns_topic_subscription", "aws"))
		}
	}
	return resources, nil
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

// subscriptionName names a subscription after where it delivers and how:
// a function's name, without its alias, the last part of another ARN
// endpoint, such as a queue's name, or the host of a URL, then the
// protocol, as in "jobs sqs".
func subscriptionName(endpoint, protocol *string) string {
	target := aws.ToString(endpoint)
	u, err := url.Parse(target)
	_, function, isFunction := strings.Cut(target, ":function:")
	switch {
	case err == nil && u.Host != "":
		target = u.Host
	case isFunction:
		target, _, _ = strings.Cut(function, ":")
	case strings.HasPrefix(target, "arn:"):
		target = snsLastPart(target)
	}
	return strings.TrimSpace(target + " " + aws.ToString(protocol))
}
