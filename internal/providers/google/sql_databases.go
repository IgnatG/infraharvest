// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/sqladmin/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// sqlDatabaseType is the Terraform type of a database in a Cloud SQL
// instance.
const sqlDatabaseType = "google_sql_database"

// cloudSQLService lists the databases of the project's Cloud SQL
// instances.
const cloudSQLService = "cloudsql"

// CloudSQLLister lists the databases of the project's Cloud SQL
// instances, the cloudsql service. Cloud Asset Inventory has no asset type
// for a database, so cloud_asset, which lists the instances themselves,
// can't find them. A region's import lists the databases of the instances
// in that region, and the import of global lists none, as no instance is
// global: each database lands in the root of its instance's region, where
// cloud_asset puts the instance. Read replicas are left out, as their
// databases are the primary's. Listing needs cloudsql.instances.list and
// cloudsql.databases.list on the project, which roles/cloudsql.viewer and
// roles/viewer grant.
type CloudSQLLister struct {
	harvest.ListerBase
}

// List lists the databases. A project without the Cloud SQL
// Admin API enabled has no instances: that is logged, and lists nothing.
// Any other failure to list the instances is an error; a failure to list
// one instance's databases, as for an instance that is stopped or still
// being created, is logged and that instance is left out.
func (g *CloudSQLLister) List() error {
	region, _ := g.Settings()["region"].(compute.Region)
	if region.Name == "" {
		return nil
	}
	project, _ := g.Settings()["project"].(string)
	ctx := g.Context()
	svc, err := sqladmin.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return err
	}
	instances, err := sqlPrimaries(ctx, svc, project, region.Name)
	if err != nil {
		if isServiceDisabled(err) {
			log.Printf("%s: cloudsql: no Cloud SQL instances listed in project %s: %v", g.Provider(), project, err)
			return nil
		}
		return err
	}
	var resources []harvest.Resource
	for _, instance := range instances {
		// Databases aren't paged: the response holds every one.
		list, err := svc.Databases.List(project, instance.Name).Context(ctx).Do()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("%s: cloudsql: the databases of instance %s in project %s can't be listed, so they are left out: %v", g.Provider(), instance.Name, project, err)
			continue
		}
		for _, db := range list.Items {
			resources = append(resources, sqlDatabase(project, instance, db.Name, g.Provider()))
		}
	}
	g.Resources = resources
	return nil
}

// sqlPrimaries returns the project's Cloud SQL instances in region whose
// databases are their own, from every page of the instance list.
func sqlPrimaries(ctx context.Context, svc *sqladmin.Service, project, region string) ([]*sqladmin.DatabaseInstance, error) {
	var instances []*sqladmin.DatabaseInstance
	err := svc.Instances.List(project).Pages(ctx, func(page *sqladmin.InstancesListResponse) error {
		for _, instance := range page.Items {
			if instance.Region == region && ownsDatabases(instance) {
				instances = append(instances, instance)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the Cloud SQL instances of project %s: %w", project, err)
	}
	return instances, nil
}

// ownsDatabases reports whether an instance's databases are its own: not
// a read replica or read pool, whose databases mirror its primary's, and
// not the stand-in for a server outside Cloud SQL that an instance
// replicates from, whose databases Cloud SQL doesn't manage.
func ownsDatabases(instance *sqladmin.DatabaseInstance) bool {
	switch instance.InstanceType {
	case "READ_REPLICA_INSTANCE", "READ_POOL_INSTANCE", "ON_PREMISES_INSTANCE":
		return false
	}
	return instance.MasterInstanceName == ""
}

// sqlDatabase is a database of instance, imported by the provider's
// projects/{project}/instances/{instance}/databases/{name} format. Its
// attributes include the instance's database version, such as MYSQL_8_0,
// so that the catalogue can tell the databases each engine creates for
// itself, such as mysql or postgres, from the user's.
func sqlDatabase(project string, instance *sqladmin.DatabaseInstance, name, provider string) harvest.Resource {
	id := "projects/" + project + "/instances/" + instance.Name + "/databases/" + name
	return harvest.Record(id, instance.Name+"-"+name, sqlDatabaseType, provider, map[string]string{
		"project":          project,
		"instance":         instance.Name,
		"name":             name,
		"database_version": instance.DatabaseVersion,
	})
}
