// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || google

package providers

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/providers/google"
)

func init() {
	define(definition{
		name:        "google",
		newProvider: func() harvest.Provider { return &google.Provider{} },
		short:       "Generate Terraform configuration for the resources of Google Cloud projects",
		long: "List the resources of the services --services names in Google Cloud projects,\n" +
			"region by region, and generate their Terraform configuration with import\n" +
			"blocks. Name the projects with --projects, or every active project of an\n" +
			"organization or folders with --organization and --folders. The global region,\n" +
			"the default, holds resources that belong to no region. Nothing is applied and\n" +
			"no state is written.",
		sampleServices: "cloud_asset,iam",
		sampleIDs:      "storage_bucket=my-logs",
		parallelScopes: "projects of --projects, --organization or --folders and regions of --regions",
		flags:          googleFlags,
	})
}

// googleScopes are the Google command's flags that aren't options: what
// widens the import to the projects of an organization or folders, and
// whether to generate for google-beta.
type googleScopes struct {
	organization string
	folders      []string
	beta         bool
}

// googleFlags adds the Google command's own flags.
func googleFlags(flags *pflag.FlagSet, options *importer.Options) (checker, planner) {
	scopes := &googleScopes{}
	flags.StringSliceVar(&options.Regions, "regions", []string{"global"}, "regions to import, each into roots of its own, such as global,europe-west1; global holds the resources that belong to no region")
	flags.StringSliceVar(&options.Projects, "projects", nil, "projects to import")
	flags.StringVar(&scopes.organization, "organization", "", "import every active project of this organization ID, in its folders too (needs resourcemanager.projects.list and folders.list, as roles/browser grants)")
	flags.StringSliceVar(&scopes.folders, "folders", nil, "import every active project of these folder IDs, in their subfolders too")
	flags.BoolVar(&scopes.beta, "beta", false, "generate configuration for the google-beta provider instead of google, for resources and arguments only it has")
	return scopes.check, scopes.plan
}

// check rejects a run that names no projects to import.
func (s *googleScopes) check(options importer.Options) error {
	if len(options.Projects) == 0 && s.organization == "" && len(s.folders) == 0 {
		return errors.New("say which projects to import: --projects, --organization or --folders")
	}
	return nil
}

// plan imports each project of --projects, --organization and --folders,
// region by region.
func (s *googleScopes) plan(ctx context.Context, options importer.Options) (plan, error) {
	projects := options.Projects
	if s.organization != "" || len(s.folders) > 0 {
		found, err := google.Projects(ctx, s.organization, s.folders)
		if err != nil {
			return plan{}, err
		}
		if len(found) == 0 {
			return plan{}, errors.New("no active projects under --organization or --folders")
		}
		log.Printf("google: %d active projects under --organization and --folders", len(found))
		projects = appendMissing(projects, found)
	}

	// A pattern with {provider}/{service} gets the project put in (see
	// below), which keeps projects apart as {account} does.
	if len(projects) > 1 && !strings.Contains(options.Layout, "{provider}/{service}") {
		if err := checkScopesApart(options.Layout, "project"); err != nil {
			return plan{}, err
		}
	}
	return plan{kind: "project", scopes: projects, calls: func(scopeOptions importer.Options, project string) []call {
		calls := make([]call, 0, len(scopeOptions.Regions))
		for _, region := range scopeOptions.Regions {
			regionOptions := scopeOptions
			regionOptions.Layout = strings.ReplaceAll(scopeOptions.Layout, "{provider}/{service}", "{provider}/"+project+"/{service}/"+region)
			calls = append(calls, call{
				options: regionOptions,
				// The provider reads the region, the project and its type.
				args:  []string{region, project, s.providerType()},
				where: "project " + project + ", region " + region,
			})
		}
		return calls
	}}, nil
}

// providerType is the provider's type, as the Google provider's Init reads
// it: "beta" for google-beta, else "".
func (s *googleScopes) providerType() string {
	if s.beta {
		return "beta"
	}
	return ""
}
