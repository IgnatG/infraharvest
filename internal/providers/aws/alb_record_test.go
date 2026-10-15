// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"
)

const (
	recordAlbNS        = "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"
	recordAlbARN       = "arn:aws:elasticloadbalancing:us-east-1:123456789012:"
	recordAlbWeb       = recordAlbARN + "loadbalancer/app/web/1a2b3c4d"
	recordAlbAPI       = recordAlbARN + "loadbalancer/app/api/5e6f7a8b"
	recordAlbHTTPS     = recordAlbARN + "listener/app/web/1a2b3c4d/aaaa1111"
	recordAlbHTTP      = recordAlbARN + "listener/app/web/1a2b3c4d/bbbb2222"
	recordAlbAdmin     = recordAlbARN + "listener/app/web/1a2b3c4d/cccc3333"
	recordAlbRule      = recordAlbARN + "listener-rule/app/web/1a2b3c4d/aaaa1111/r1"
	recordAlbDefault   = recordAlbARN + "listener-rule/app/web/1a2b3c4d/aaaa1111/default"
	recordAlbCert      = "arn:aws:acm:us-east-1:123456789012:certificate/"
	recordAlbWebGroup  = recordAlbARN + "targetgroup/web/9c8b7a6d"
	recordAlbAPIGroup  = recordAlbARN + "targetgroup/api/5f4e3d2c"
	recordAlbCertsPage = "certs-page-2"
)

// The lister lists, in order, each load balancer with its listeners, each
// listener followed by its rules but the default one and by its extra
// certificates; then each target group with its registered targets, by
// Terraform's import ID. Load balancers, listeners, certificates and
// target groups are read across pages. The rules' pagination is pinned by
// TestAlbListenerRulesStopOnEmptyMarker. An error for one load balancer,
// listener or target group leaves out only its part, and is logged.
func TestRecordAlbListsBalancersListenersAndTargetGroups(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		switch call.Op {
		case "DescribeLoadBalancers":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordAlbNS, `<LoadBalancers><member>
					<LoadBalancerArn>`+recordAlbAPI+`</LoadBalancerArn><LoadBalancerName>api</LoadBalancerName><Type>application</Type>
					</member></LoadBalancers>`)
			}
			return recordQuery(call.Op, recordAlbNS, `<LoadBalancers><member>
				<LoadBalancerArn>`+recordAlbWeb+`</LoadBalancerArn><LoadBalancerName>web</LoadBalancerName><Type>application</Type>
				</member></LoadBalancers><NextMarker>`+nextPageToken+`</NextMarker>`)
		case "DescribeListeners":
			switch form.Get("LoadBalancerArn") {
			case recordAlbWeb:
				if recordSecondPage(call) {
					return recordQuery(call.Op, recordAlbNS, `<Listeners>
						<member><ListenerArn>`+recordAlbHTTP+`</ListenerArn><Protocol>HTTP</Protocol><Port>80</Port></member>
						<member><ListenerArn>`+recordAlbAdmin+`</ListenerArn><Protocol>HTTPS</Protocol><Port>8443</Port>
							<Certificates><member><CertificateArn>`+recordAlbCert+`admin</CertificateArn></member></Certificates></member>
						</Listeners>`)
				}
				return recordQuery(call.Op, recordAlbNS, `<Listeners><member>
					<ListenerArn>`+recordAlbHTTPS+`</ListenerArn><Protocol>HTTPS</Protocol><Port>443</Port>
					<Certificates><member><CertificateArn>`+recordAlbCert+`default</CertificateArn></member></Certificates>
					</member></Listeners><NextMarker>`+nextPageToken+`</NextMarker>`)
			case recordAlbAPI:
				// Logged; the load balancer stays, without listeners.
				return recordQueryError(recordAlbNS, "AccessDenied")
			}
		case "DescribeRules":
			if form.Get("PageSize") != "400" {
				t.Errorf("DescribeRules page size %q, want 400", form.Get("PageSize"))
			}
			switch form.Get("ListenerArn") {
			case recordAlbHTTPS:
				return recordQuery(call.Op, recordAlbNS, `<Rules>
					<member><RuleArn>`+recordAlbDefault+`</RuleArn><IsDefault>true</IsDefault><Priority>default</Priority></member>
					<member><RuleArn>`+recordAlbRule+`</RuleArn><IsDefault>false</IsDefault><Priority>10</Priority></member>
					</Rules>`)
			case recordAlbHTTP:
				// Logged; the listener's certificates are still listed.
				return recordQueryError(recordAlbNS, "AccessDenied")
			case recordAlbAdmin:
				return recordQuery(call.Op, recordAlbNS, `<Rules/>`)
			}
		case "DescribeListenerCertificates":
			switch form.Get("ListenerArn") {
			case recordAlbHTTPS:
				if form.Get("Marker") == recordAlbCertsPage {
					// The default certificate is the one marked so, even
					// past the first page.
					return recordQuery(call.Op, recordAlbNS, `<Certificates>
						<member><CertificateArn>`+recordAlbCert+`more</CertificateArn><IsDefault>false</IsDefault></member>
						</Certificates>`)
				}
				return recordQuery(call.Op, recordAlbNS, `<Certificates>
					<member><CertificateArn>`+recordAlbCert+`default</CertificateArn><IsDefault>true</IsDefault></member>
					<member><CertificateArn>`+recordAlbCert+`extra</CertificateArn><IsDefault>false</IsDefault></member>
					</Certificates><NextMarker>`+recordAlbCertsPage+`</NextMarker>`)
			case recordAlbHTTP:
				return recordQuery(call.Op, recordAlbNS, `<Certificates/>`)
			case recordAlbAdmin:
				// Logged; the listener stays.
				return recordQueryError(recordAlbNS, "AccessDenied")
			}
		case "DescribeTargetGroups":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordAlbNS, `<TargetGroups><member>
					<TargetGroupArn>`+recordAlbAPIGroup+`</TargetGroupArn><TargetGroupName>api</TargetGroupName><TargetType>ip</TargetType>
					</member></TargetGroups>`)
			}
			return recordQuery(call.Op, recordAlbNS, `<TargetGroups><member>
				<TargetGroupArn>`+recordAlbWebGroup+`</TargetGroupArn><TargetGroupName>web</TargetGroupName><TargetType>instance</TargetType>
				</member></TargetGroups><NextMarker>`+nextPageToken+`</NextMarker>`)
		case "DescribeTargetHealth":
			switch form.Get("TargetGroupArn") {
			case recordAlbWebGroup:
				return recordQuery(call.Op, recordAlbNS, `<TargetHealthDescriptions>
					<member><Target><Id>i-0a1</Id><Port>80</Port></Target><TargetHealth><State>healthy</State></TargetHealth></member>
					<member><Target><Id>10.1.2.3</Id><Port>8080</Port><AvailabilityZone>all</AvailabilityZone></Target><TargetHealth><State>unhealthy</State></TargetHealth></member>
					<member><Target><Id>arn:aws:lambda:us-east-1:123456789012:function:hook</Id></Target><TargetHealth><State>unavailable</State></TargetHealth></member>
					</TargetHealthDescriptions>`)
			case recordAlbAPIGroup:
				// Logged; the target group stays, without targets.
				return recordQueryError(recordAlbNS, "AccessDenied")
			}
		}
		return recordUnexpected(t, call)
	})
	g := &AlbLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_lb", recordAlbWeb, "web", nil},
		recordedResource{"aws_lb_listener", recordAlbHTTPS, recordAlbHTTPS, nil},
		recordedResource{"aws_lb_listener_rule", recordAlbRule, recordAlbRule, nil},
		recordedResource{"aws_lb_listener_certificate", recordAlbHTTPS + "_" + recordAlbCert + "extra", recordAlbHTTPS + "_" + recordAlbCert + "extra",
			map[string]string{"listener_arn": recordAlbHTTPS, "certificate_arn": recordAlbCert + "extra"}},
		recordedResource{"aws_lb_listener_certificate", recordAlbHTTPS + "_" + recordAlbCert + "more", recordAlbHTTPS + "_" + recordAlbCert + "more",
			map[string]string{"listener_arn": recordAlbHTTPS, "certificate_arn": recordAlbCert + "more"}},
		recordedResource{"aws_lb_listener", recordAlbHTTP, recordAlbHTTP, nil},
		recordedResource{"aws_lb_listener", recordAlbAdmin, recordAlbAdmin, nil},
		recordedResource{"aws_lb", recordAlbAPI, "api", nil},
		recordedResource{"aws_lb_target_group", recordAlbWebGroup, "web", nil},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",i-0a1,80", recordAlbWebGroup + ",i-0a1,80",
			map[string]string{"target_id": "i-0a1", "target_group_arn": recordAlbWebGroup, "port": "80"}},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",10.1.2.3,8080,all", recordAlbWebGroup + ",10.1.2.3,8080,all",
			map[string]string{"target_id": "10.1.2.3", "target_group_arn": recordAlbWebGroup, "port": "8080", "availability_zone": "all"}},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",arn:aws:lambda:us-east-1:123456789012:function:hook",
			recordAlbWebGroup + ",arn:aws:lambda:us-east-1:123456789012:function:hook",
			map[string]string{"target_id": "arn:aws:lambda:us-east-1:123456789012:function:hook", "target_group_arn": recordAlbWebGroup}},
		recordedResource{"aws_lb_target_group", recordAlbAPIGroup, "api", nil},
	)
}

// Listing load balancers or target groups fails the service.
func TestRecordAlbReturnsErrors(t *testing.T) {
	for _, failing := range []string{"DescribeLoadBalancers", "DescribeTargetGroups"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordAlbNS, "AccessDenied")
				case "DescribeLoadBalancers":
					return recordQuery(call.Op, recordAlbNS, `<LoadBalancers/>`)
				}
				return recordUnexpected(t, call)
			})
			g := &AlbLister{}

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}
