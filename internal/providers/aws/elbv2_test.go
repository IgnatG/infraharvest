// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const (
	recordAlbNS        = "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"
	recordAlbARN       = "arn:aws:elasticloadbalancing:us-east-1:123456789012:"
	recordAlbWeb       = recordAlbARN + "loadbalancer/app/web/1a2b3c4d"
	recordAlbAPI       = recordAlbARN + "loadbalancer/app/api/5e6f7a8b"
	recordAlbHTTPS     = recordAlbARN + "listener/app/web/1a2b3c4d/aaaa1111"
	recordAlbHTTP      = recordAlbARN + "listener/app/web/1a2b3c4d/bbbb2222"
	recordAlbAdmin     = recordAlbARN + "listener/app/web/1a2b3c4d/cccc3333"
	recordAlbTCP       = recordAlbARN + "listener/app/web/1a2b3c4d/dddd4444"
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
// listener or target group leaves out only its part, and the failures are
// returned together as a partial error naming each part. Only an
// HTTPS or TLS listener, or one with certificates, is asked for its
// certificates, and a TCP, TLS, UDP or GENEVE listener, which has no rules
// but its default one, isn't asked for rules.
func TestRecordAlbListsBalancersListenersAndTargetGroups(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
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
						<member><ListenerArn>`+recordAlbTCP+`</ListenerArn><Protocol>TCP</Protocol><Port>5432</Port></member>
						<member><ListenerArn>`+recordAlbAdmin+`</ListenerArn><Protocol>HTTPS</Protocol><Port>8443</Port>
							<Certificates><member><CertificateArn>`+recordAlbCert+`admin</CertificateArn></member></Certificates></member>
						</Listeners>`)
				}
				return recordQuery(call.Op, recordAlbNS, `<Listeners><member>
					<ListenerArn>`+recordAlbHTTPS+`</ListenerArn><Protocol>HTTPS</Protocol><Port>443</Port>
					<Certificates><member><CertificateArn>`+recordAlbCert+`default</CertificateArn></member></Certificates>
					</member></Listeners><NextMarker>`+nextPageToken+`</NextMarker>`)
			case recordAlbAPI:
				// A part that fails; the load balancer stays, without listeners.
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
				// A part that fails; the listener stays.
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
			case recordAlbAdmin:
				// A part that fails; the listener stays.
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
				// A part that fails; the target group stays, without targets.
				return recordQueryError(recordAlbNS, "AccessDenied")
			}
		}
		return recordUnexpected(t, call)
	})
	g := &AlbLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if !harvest.IsPartial(err) {
		t.Errorf("want a partial error, got %v", err)
	}
	for _, part := range []string{"listeners of " + recordAlbAPI, "rules of listener " + recordAlbHTTP, "certificates of listener " + recordAlbAdmin, "targets of target group " + recordAlbAPIGroup} {
		if err == nil || !strings.Contains(err.Error(), part) {
			t.Errorf("the error doesn't name the %s: %v", part, err)
		}
	}

	assertRecorded(t, resources,
		recordedResource{"aws_lb", recordAlbWeb, "web", nil},
		recordedResource{"aws_lb_listener", recordAlbHTTPS, "HTTPS 443", nil},
		recordedResource{"aws_lb_listener_rule", recordAlbRule, "HTTPS 443 10", nil},
		recordedResource{"aws_lb_listener_certificate", recordAlbHTTPS + "_" + recordAlbCert + "extra", "HTTPS 443",
			map[string]string{"listener_arn": recordAlbHTTPS, "certificate_arn": recordAlbCert + "extra"}},
		recordedResource{"aws_lb_listener_certificate", recordAlbHTTPS + "_" + recordAlbCert + "more", "HTTPS 443",
			map[string]string{"listener_arn": recordAlbHTTPS, "certificate_arn": recordAlbCert + "more"}},
		recordedResource{"aws_lb_listener", recordAlbHTTP, "HTTP 80", nil},
		recordedResource{"aws_lb_listener", recordAlbTCP, "TCP 5432", nil},
		recordedResource{"aws_lb_listener", recordAlbAdmin, "HTTPS 8443", nil},
		recordedResource{"aws_lb", recordAlbAPI, "api", nil},
		recordedResource{"aws_lb_target_group", recordAlbWebGroup, "web", nil},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",i-0a1,80", "web i-0a1",
			map[string]string{"target_id": "i-0a1", "target_group_arn": recordAlbWebGroup, "port": "80"}},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",10.1.2.3,8080,all", "web 10.1.2.3",
			map[string]string{"target_id": "10.1.2.3", "target_group_arn": recordAlbWebGroup, "port": "8080", "availability_zone": "all"}},
		recordedResource{"aws_lb_target_group_attachment", recordAlbWebGroup + ",arn:aws:lambda:us-east-1:123456789012:function:hook",
			"web hook",
			map[string]string{"target_id": "arn:aws:lambda:us-east-1:123456789012:function:hook", "target_group_arn": recordAlbWebGroup}},
		recordedResource{"aws_lb_target_group", recordAlbAPIGroup, "api", nil},
	)
}

// Listing load balancers or target groups fails the service.
func TestRecordAlbReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"DescribeLoadBalancers", "DescribeTargetGroups"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordAlbNS, "AccessDenied")
				case "DescribeLoadBalancers":
					return recordQuery(call.Op, recordAlbNS, `<LoadBalancers/>`)
				}
				return recordUnexpected(t, call)
			})
			g := &AlbLister{}

			assertRecordedError(t, listError(ctx, g), "AccessDenied")
		})
	}
}
