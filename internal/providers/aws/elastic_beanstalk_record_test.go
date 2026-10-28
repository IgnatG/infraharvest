// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"
)

const (
	recordBeanstalkNS       = "http://elasticbeanstalk.amazonaws.com/docs/2010-12-01/"
	recordBeanstalkMoreEnvs = "environments-page-2"
)

// The lister lists applications by name, then environments by ID, named
// after their name, across pages, leaving out the terminated ones.
func TestRecordBeanstalkListsApplicationsAndEnvironments(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		switch call.Op {
		case "DescribeApplications":
			return recordQuery(call.Op, recordBeanstalkNS, `<Applications>
				<member><ApplicationName>web</ApplicationName><ApplicationArn>arn:aws:elasticbeanstalk:us-east-1:123456789012:application/web</ApplicationArn></member>
				<member><ApplicationName>worker</ApplicationName></member>
				</Applications>`)
		case "DescribeEnvironments":
			if form.Get("IncludeDeleted") != "false" {
				t.Errorf("environments listed with the deleted ones: %s", call.Body)
			}
			if form.Get("NextToken") == recordBeanstalkMoreEnvs {
				return recordQuery(call.Op, recordBeanstalkNS, `<Environments>
					<member><EnvironmentId>e-ghi012</EnvironmentId><EnvironmentName>web-staging</EnvironmentName><ApplicationName>web</ApplicationName><Status>Updating</Status></member>
					<member><EnvironmentId>e-jkl345</EnvironmentId><EnvironmentName>web-going</EnvironmentName><ApplicationName>web</ApplicationName><Status>Terminating</Status></member>
					</Environments>`)
			}
			return recordQuery(call.Op, recordBeanstalkNS, `<Environments>
				<member><EnvironmentId>e-abc123</EnvironmentId><EnvironmentName>web-prod</EnvironmentName><ApplicationName>web</ApplicationName><Status>Ready</Status></member>
				<member><EnvironmentId>e-def456</EnvironmentId><EnvironmentName>worker-prod</EnvironmentName><ApplicationName>worker</ApplicationName><Status>Ready</Status></member>
				<member><EnvironmentId>e-old789</EnvironmentId><EnvironmentName>web-old</EnvironmentName><ApplicationName>web</ApplicationName><Status>Terminated</Status></member>
				</Environments><NextToken>`+recordBeanstalkMoreEnvs+`</NextToken>`)
		}
		return recordUnexpected(t, call)
	})
	g := &BeanstalkLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_elastic_beanstalk_application", "web", "web", nil},
		recordedResource{"aws_elastic_beanstalk_application", "worker", "worker", nil},
		recordedResource{"aws_elastic_beanstalk_environment", "e-abc123", "web-prod", nil},
		recordedResource{"aws_elastic_beanstalk_environment", "e-def456", "worker-prod", nil},
		recordedResource{"aws_elastic_beanstalk_environment", "e-ghi012", "web-staging", nil},
	)
}

// Each list fails the service.
func TestRecordBeanstalkReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"DescribeApplications", "DescribeEnvironments"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordBeanstalkNS, "InsufficientPrivilegesException")
				case "DescribeApplications":
					return recordQuery(call.Op, recordBeanstalkNS, `<Applications/>`)
				}
				return recordUnexpected(t, call)
			})
			g := &BeanstalkLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "InsufficientPrivilegesException")
		})
	}
}
