// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"google.golang.org/api/googleapi"
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
// Any other failure to list the instances is an error. An instance deleted
// since it was listed has no databases, and is logged and left out. One
// that can't serve the database list, as one that is stopped or still
// being created (see sqlInstanceNotServing), still has its databases: it
// is logged and left out, and the import's google_sql_database becomes a
// type the provider lists in part (see Provider.PartialTypes), so that
// an incremental import doesn't take those databases for deleted, while
// the rest of the run is compared as usual. Any other failure to list one
// instance's databases, such as a permission denied, fails the listing in
// part (see harvest.Partial): the other instances' databases are kept,
// and the run says that some are missing rather than reading them as
// deleted.
func (g *CloudSQLLister) List() error {
	region := regionSetting(g.Settings())
	if region.Name == "" {
		return nil
	}
	project, err := projectSetting(g.Settings())
	if err != nil {
		return err
	}
	// What an earlier listing of the import left out isn't this one's.
	setSQLDatabasesUnlisted(project, region.Name, false)
	// The databases take a call per instance, and the instances one more.
	if !wants(g.Settings(), sqlDatabaseType) {
		return nil
	}
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
	var unlisted []error
	for _, instance := range instances {
		// Databases aren't paged: the response holds every one.
		list, err := svc.Databases.List(project, instance.Name).Context(ctx).Do()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if sqlInstanceGone(err) {
				log.Printf("%s: cloudsql: instance %s in project %s was deleted since it was listed, so it has no databases to list: %v", g.Provider(), instance.Name, project, err)
				continue
			}
			if sqlInstanceNotServing(err) {
				log.Printf("%s: cloudsql: the databases of instance %s in project %s can't be listed, so they are left out, and not taken for deleted: %v", g.Provider(), instance.Name, project, err)
				setSQLDatabasesUnlisted(project, region.Name, true)
				continue
			}
			unlisted = append(unlisted, fmt.Errorf("listing the databases of Cloud SQL instance %s in project %s: %w", instance.Name, project, err))
			continue
		}
		for _, db := range list.Items {
			resources = append(resources, sqlDatabase(project, instance, db.Name, g.Provider()))
		}
	}
	g.Resources = resources
	return harvest.Partial(errors.Join(unlisted...))
}

// sqlInstanceNotServing reports whether err says a Cloud SQL instance
// can't serve a call in the state it is in, rather than that the call
// failed: a 409, which the SQL Admin API answers with the reason
// invalidState for an instance not in a state to handle the request, such
// as one being created, or operationInProgress while another operation
// runs; or a 400 saying the instance is not running, as for one that is
// stopped, whose reason is only invalid. Such an instance still has its
// databases.
func sqlInstanceNotServing(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case http.StatusConflict:
		return true
	case http.StatusBadRequest:
		return strings.Contains(strings.ToLower(apiErr.Message), "not running")
	}
	return false
}

// sqlInstanceGone reports whether err is a 404, which the SQL Admin API
// answers for an instance deleted since it was listed: it has no
// databases left.
func sqlInstanceGone(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}

// unlistedSQLDatabases are the imports, by where they went (an emulator,
// or Google), project and region, whose last listing of the databases
// left out those of an instance that couldn't serve the list (see
// sqlInstanceNotServing). The process keeps them, as rejectedTypes, since
// the lister and the provider asked for its partial types are not the
// same: every service of an import has a provider of its own. Services
// list at once, so a lock guards them.
var unlistedSQLDatabases = struct {
	sync.Mutex
	byKey map[string]bool
}{byKey: map[string]bool{}}

// sqlDatabasesKey names the import of project's region, where it went.
func sqlDatabasesKey(project, region string) string {
	return os.Getenv(EndpointEnv) + "\x00" + project + "\x00" + region
}

// setSQLDatabasesUnlisted records whether the listing of the databases in
// project's region left out some (see unlistedSQLDatabases).
func setSQLDatabasesUnlisted(project, region string, unlisted bool) {
	unlistedSQLDatabases.Lock()
	defer unlistedSQLDatabases.Unlock()
	if unlisted {
		unlistedSQLDatabases.byKey[sqlDatabasesKey(project, region)] = true
	} else {
		delete(unlistedSQLDatabases.byKey, sqlDatabasesKey(project, region))
	}
}

// sqlDatabasesUnlisted reports whether the last listing of the databases
// in project's region left out some (see unlistedSQLDatabases).
func sqlDatabasesUnlisted(project, region string) bool {
	unlistedSQLDatabases.Lock()
	defer unlistedSQLDatabases.Unlock()
	return unlistedSQLDatabases.byKey[sqlDatabasesKey(project, region)]
}

// sqlPrimaries returns the project's Cloud SQL instances in region whose
// databases are their own, from the project's instance list (see
// sqlInstancesOnce).
func sqlPrimaries(ctx context.Context, svc *sqladmin.Service, project, region string) ([]*sqladmin.DatabaseInstance, error) {
	all, err := sqlInstancesOnce(ctx, svc, project)
	if err != nil {
		return nil, err
	}
	var instances []*sqladmin.DatabaseInstance
	for _, instance := range all {
		if instance.Region == region && ownsDatabases(instance) {
			instances = append(instances, instance)
		}
	}
	return instances, nil
}

// sqlInstanceLists are the instance lists the process has made, by
// sqlInstancesKey: the list has every region's instances, and one command
// imports a project region by region, so it is listed once for all of
// them rather than once for each, as searchOnce searches.
var sqlInstanceLists projectLists[[]*sqladmin.DatabaseInstance]

// sqlInstancesOnce returns the project's Cloud SQL instances, from every
// page of the instance list, listing them only the first time the process
// asks (see projectLists): the imports of a project's regions share the
// list, which they only read. A list that fails isn't kept, so the next
// import lists again.
func sqlInstancesOnce(ctx context.Context, svc *sqladmin.Service, project string) ([]*sqladmin.DatabaseInstance, error) {
	return sqlInstanceLists.once(project, func() ([]*sqladmin.DatabaseInstance, error) {
		var instances []*sqladmin.DatabaseInstance
		err := svc.Instances.List(project).Pages(ctx, func(page *sqladmin.InstancesListResponse) error {
			instances = append(instances, page.Items...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing the Cloud SQL instances of project %s: %w", project, err)
		}
		return instances, nil
	})
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
