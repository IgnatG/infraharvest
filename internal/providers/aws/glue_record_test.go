// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

const recordGlueSTSNS = "https://sts.amazonaws.com/doc/2011-06-15/"

// recordGlueAPI answers STS with account 123456789012 and Glue with two
// pages of each list; database sales has two pages of tables, database
// logs none. The operation failing answers with an error.
func recordGlueAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing && failing == "GetCallerIdentity" {
			return recordQueryError(recordGlueSTSNS, "AccessDenied")
		}
		if failing != "" && call.Op == failing {
			return recordJSONError("AccessDeniedException")
		}
		page := func(key, first, second string) string {
			if recordSecondPage(call) {
				return fmt.Sprintf(`{%q:[%s]}`, key, second)
			}
			return fmt.Sprintf(`{%q:[%s],"NextToken":%q}`, key, first, nextPageToken)
		}
		switch call.Op {
		case "GetCallerIdentity":
			return recordQuery(call.Op, recordGlueSTSNS, `<Account>123456789012</Account>
				<Arn>arn:aws:iam::123456789012:user/auditor</Arn><UserId>AIDAEXAMPLE</UserId>`)
		case "GetCrawlers":
			return page("Crawlers", `{"Name":"sales-crawler","Role":"glue","DatabaseName":"sales"}`, `{"Name":"logs-crawler","Role":"glue"}`)
		case "GetDatabases":
			return page("DatabaseList", `{"Name":"sales","CatalogId":"123456789012"}`, `{"Name":"logs","CatalogId":"123456789012"}`)
		case "GetTables":
			switch recordJSONField(call, "DatabaseName") {
			case "sales":
				return page("TableList", `{"Name":"orders","DatabaseName":"sales"}`, `{"Name":"customers","DatabaseName":"sales"}`)
			case "logs":
				return `{"TableList":[]}`
			}
		case "GetJobs":
			return page("Jobs", `{"Name":"load-orders","Role":"glue","Command":{"Name":"glueetl"}}`, `{"Name":"compact","Role":"glue"}`)
		case "GetTriggers":
			return page("Triggers", `{"Name":"nightly","Type":"SCHEDULED","Schedule":"cron(0 2 * * ? *)"}`, `{"Name":"on-demand","Type":"ON_DEMAND"}`)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists, each across pages and in this order: crawlers by name,
// catalog databases as "<account>:<database>", then the tables of each
// database as "<account>:<database>:<table>", named "<database>:<table>",
// then jobs and triggers by name.
func TestRecordGlueListsCrawlersCatalogJobsAndTriggers(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordGlueAPI(t, ""))
	g := &GlueLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_glue_crawler", "sales-crawler", "sales-crawler", nil},
		recordedResource{"aws_glue_crawler", "logs-crawler", "logs-crawler", nil},
		recordedResource{"aws_glue_catalog_database", "123456789012:sales", "sales", nil},
		recordedResource{"aws_glue_catalog_database", "123456789012:logs", "logs", nil},
		recordedResource{"aws_glue_catalog_table", "123456789012:sales:orders", "sales:orders", nil},
		recordedResource{"aws_glue_catalog_table", "123456789012:sales:customers", "sales:customers", nil},
		recordedResource{"aws_glue_job", "load-orders", "load-orders", nil},
		recordedResource{"aws_glue_job", "compact", "compact", nil},
		recordedResource{"aws_glue_trigger", "nightly", "nightly", nil},
		recordedResource{"aws_glue_trigger", "on-demand", "on-demand", nil},
	)
}

// The account and each list fail the service.
func TestRecordGlueReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"GetCallerIdentity", "GetCrawlers", "GetDatabases", "GetTables", "GetJobs", "GetTriggers"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordGlueAPI(t, failing))
			g := &GlueLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}
