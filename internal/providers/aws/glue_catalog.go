// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// GlueLister lists the region's Glue crawlers, Data Catalog databases
// and tables, jobs and triggers.
type GlueLister struct {
	Service
}

// List lists, in this order, the crawlers, the catalog databases,
// the tables of each database, the jobs and the triggers. Terraform imports
// catalog databases and tables by an ID that starts with the catalog ID,
// which is the account's, so the account is looked up first. A database
// whose tables can't be read keeps those read before the failure, and the
// failure makes the listing partial (see harvest.Parts); a database
// deleted since it was listed has none. Any other failure, the account
// lookup included, fails the service.
func (g *GlueLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *GlueLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	accountID, err := g.callerAccount(config)
	if err != nil {
		return err
	}
	account := aws.ToString(accountID)
	client := glue.NewFromConfig(config)
	ctx := g.Context()
	if err := g.listCrawlers(ctx, client); err != nil {
		return err
	}
	databases, err := g.listDatabases(ctx, client, account)
	if err != nil {
		return err
	}
	// A call per database, for tables the import may not keep.
	if !g.Wants("aws_glue_catalog_table") {
		databases = nil
	}
	var parts harvest.Parts
	for _, database := range databases {
		parts.Add(awsCodes, "the tables of database "+database, g.listTables(ctx, client, account, database))
	}
	if err := g.listJobs(ctx, client); err != nil {
		return parts.Fail(err)
	}
	if err := g.listTriggers(ctx, client); err != nil {
		return parts.Fail(err)
	}
	return parts.Err()
}

// listCrawlers adds each crawler as an aws_glue_crawler, imported by its
// name.
func (g *GlueLister) listCrawlers(ctx context.Context, client *glue.Client) error {
	for page, err := range pagesOf(ctx, glue.NewGetCrawlersPaginator(client, &glue.GetCrawlersInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, crawler := range page.Crawlers {
			name := aws.ToString(crawler.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_glue_crawler", "aws"))
		}
	}
	return nil
}

// listDatabases adds each catalog database as an aws_glue_catalog_database,
// imported as "<account>:<database>" and named after the database, and
// returns the databases' names for listing their tables.
func (g *GlueLister) listDatabases(ctx context.Context, client *glue.Client, account string) ([]string, error) {
	var databases []string
	for page, err := range pagesOf(ctx, glue.NewGetDatabasesPaginator(client, &glue.GetDatabasesInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return nil, err
		}
		for _, database := range page.DatabaseList {
			name := aws.ToString(database.Name)
			databases = append(databases, name)
			g.Resources = append(g.Resources, harvest.RecordBare(account+":"+name, name, "aws_glue_catalog_database", "aws"))
		}
	}
	return databases, nil
}

// listTables adds each table of database as an aws_glue_catalog_table,
// imported as "<account>:<database>:<table>" and named
// "<database>:<table>", since table names repeat across databases.
func (g *GlueLister) listTables(ctx context.Context, client *glue.Client, account, database string) error {
	for page, err := range pagesOf(ctx, glue.NewGetTablesPaginator(client, &glue.GetTablesInput{DatabaseName: aws.String(database)}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, table := range page.TableList {
			name := database + ":" + aws.ToString(table.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(account+":"+name, name, "aws_glue_catalog_table", "aws"))
		}
	}
	return nil
}

// listJobs adds each job as an aws_glue_job, imported by its name.
func (g *GlueLister) listJobs(ctx context.Context, client *glue.Client) error {
	for page, err := range pagesOf(ctx, glue.NewGetJobsPaginator(client, &glue.GetJobsInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, job := range page.Jobs {
			name := aws.ToString(job.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_glue_job", "aws"))
		}
	}
	return nil
}

// listTriggers adds each trigger as an aws_glue_trigger, imported by its
// name.
func (g *GlueLister) listTriggers(ctx context.Context, client *glue.Client) error {
	for page, err := range pagesOf(ctx, glue.NewGetTriggersPaginator(client, &glue.GetTriggersInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, trigger := range page.Triggers {
			name := aws.ToString(trigger.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_glue_trigger", "aws"))
		}
	}
	return nil
}
