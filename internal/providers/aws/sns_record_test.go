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
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
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
	g.SetContext(ctx)

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
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordQueryError(recordSNSNS, "AuthorizationError")
	})
	g := &SnsLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "AuthorizationError")
}

// The topics' subscriptions are asked for several topics at once, and
// recorded in the listed order. A topic whose subscriptions fail fails the
// service, with the topics before it, and itself, recorded.
func TestRecordSnsReadsTopicsInParallel(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"", "b"} {
		at := newOverlap(3)
		ctx := useFakeAPI(t, func(call apiCall) string {
			switch call.Op {
			case "ListTopics":
				return recordQuery(call.Op, recordSNSNS, `<Topics>`+
					`<member><TopicArn>`+recordSNSARN+`a</TopicArn></member>`+
					`<member><TopicArn>`+recordSNSARN+`b</TopicArn></member>`+
					`<member><TopicArn>`+recordSNSARN+`c</TopicArn></member>`+
					`</Topics>`)
			case "ListSubscriptionsByTopic":
				at.wait()
				form, _ := url.ParseQuery(call.Body)
				topic := snsLastPart(form.Get("TopicArn"))
				if topic == failing {
					return recordQueryError(recordSNSNS, "AuthorizationError")
				}
				return recordQuery(call.Op, recordSNSNS, `<Subscriptions>`+
					recordSNSSubscription(recordSNSARN+topic+":sub", "sqs", recordSNSARN+topic+"-queue")+
					`</Subscriptions>`)
			}
			return recordUnexpected(t, call)
		})
		g := &SnsLister{}
		g.SetContext(ctx)

		err := g.List()

		at.check(t)
		topic := func(name string) recordedResource {
			return recordedResource{"aws_sns_topic", recordSNSARN + name, name, nil}
		}
		subscription := func(name string) recordedResource {
			return recordedResource{"aws_sns_topic_subscription", recordSNSARN + name + ":sub", name + "-queue sqs", nil}
		}
		if failing == "" {
			if err != nil {
				t.Fatal(err)
			}
			assertRecorded(t, g.Resources, topic("a"), subscription("a"), topic("b"), subscription("b"), topic("c"), subscription("c"))
			continue
		}
		assertRecordedError(t, err, "AuthorizationError")
		assertRecorded(t, g.Resources, topic("a"), subscription("a"), topic("b"))
	}
}
