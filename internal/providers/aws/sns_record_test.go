// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"
)

const (
	recordSNSNS  = "http://sns.amazonaws.com/doc/2010-03-31/"
	recordSNSARN = "arn:aws:sns:us-east-1:123456789012:"
)

func recordSNSSubscription(id, protocol, endpoint string) string {
	return `<member><SubscriptionArn>` + id + `</SubscriptionArn><Protocol>` + protocol + `</Protocol><Owner>123456789012</Owner>` +
		`<TopicArn>` + recordSNSARN + `alerts</TopicArn><Endpoint>` + endpoint + `</Endpoint></member>`
}

// The lister lists topics by ARN, named after their last part, across
// pages, each followed by its subscriptions, across pages, by ARN and named
// after their endpoint and protocol, not after their IDs as before. It
// leaves out email subscriptions and pending ones, which Terraform can't
// manage. TestSnsSubscriptionsStopOnError
// (pagination_test.go) pins that a subscription listing error is returned.
func TestRecordSnsListsTopicsAndSubscriptions(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListTopics":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordSNSNS, `<Topics><member><TopicArn>`+recordSNSARN+`orders.fifo</TopicArn></member></Topics>`)
			}
			return recordQuery(call.Op, recordSNSNS, `<Topics><member><TopicArn>`+recordSNSARN+`alerts</TopicArn></member></Topics><NextToken>`+nextPageToken+`</NextToken>`)
		case "ListSubscriptionsByTopic":
			form, _ := url.ParseQuery(call.Body)
			switch form.Get("TopicArn") {
			case recordSNSARN + "alerts":
				if recordSecondPage(call) {
					return recordQuery(call.Op, recordSNSNS, `<Subscriptions>`+
						recordSNSSubscription("PendingConfirmation", "sqs", recordSNSARN+"queue")+
						recordSNSSubscription(recordSNSARN+"alerts:sub-4", "email-json", "ops@example.com")+
						recordSNSSubscription(recordSNSARN+"alerts:sub-5", "https", "https://hooks.example.com/sns")+
						`</Subscriptions>`)
				}
				return recordQuery(call.Op, recordSNSNS, `<Subscriptions>`+
					recordSNSSubscription(recordSNSARN+"alerts:sub-1", "lambda", "arn:aws:lambda:us-east-1:123456789012:function:notify:live")+
					recordSNSSubscription(recordSNSARN+"alerts:sub-2", "email", "ops@example.com")+
					`</Subscriptions><NextToken>`+nextPageToken+`</NextToken>`)
			case recordSNSARN + "orders.fifo":
				return recordQuery(call.Op, recordSNSNS, `<Subscriptions/>`)
			}
		}
		return recordUnexpected(t, call)
	})
	g := &SnsLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_sns_topic", recordSNSARN + "alerts", "alerts", nil},
		recordedResource{"aws_sns_topic_subscription", recordSNSARN + "alerts:sub-1", "notify lambda", nil},
		recordedResource{"aws_sns_topic_subscription", recordSNSARN + "alerts:sub-5", "hooks.example.com https", nil},
		recordedResource{"aws_sns_topic", recordSNSARN + "orders.fifo", "orders.fifo", nil},
	)
}

func TestRecordSnsReturnsListTopicsError(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordQueryError(recordSNSNS, "AuthorizationError")
	})
	g := &SnsLister{}

	assertRecordedError(t, g.List(), "AuthorizationError")
}
