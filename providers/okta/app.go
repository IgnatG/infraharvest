// Copyright 2021 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package okta

import (
	"context"

	"github.com/okta/okta-sdk-golang/v5/okta"
)

// oktaApp is what the app listers need of an application, whatever its
// sign-on mode.
type oktaApp struct {
	ID         string
	Name       string
	SignOnMode string
}

// unsupportedAppNames are app names the Okta Terraform provider cannot manage.
var unsupportedAppNames = map[string]bool{
	"template_wsfed":        true,
	"template_swa_two_page": true,
	"okta_enduser":          true,
	"okta_browser_plugin":   true,
	"saasure":               true,
}

// getApplications returns the supported applications with signOnMode. The
// applications API filters by name or status, not by sign-on mode, so the
// filter is applied here.
func getApplications(ctx context.Context, client *okta.APIClient, signOnMode string) ([]oktaApp, error) {
	apps, err := getAllApplications(ctx, client)
	if err != nil {
		return nil, err
	}
	return appsWithSignOnMode(apps, signOnMode), nil
}

func appsWithSignOnMode(apps []oktaApp, signOnMode string) []oktaApp {
	var filtered []oktaApp
	for _, app := range apps {
		if app.SignOnMode == signOnMode {
			filtered = append(filtered, app)
		}
	}
	return filtered
}

// getAllApplications returns every application the Okta provider supports.
func getAllApplications(ctx context.Context, client *okta.APIClient) ([]oktaApp, error) {
	apps, err := allPages(client.ApplicationAPI.ListApplications(ctx).Execute())
	if err != nil {
		return nil, err
	}
	return supportedApps(apps), nil
}

func supportedApps(apps []okta.ListApplications200ResponseInner) []oktaApp {
	var supported []oktaApp
	for i := range apps {
		app, ok := toOktaApp(&apps[i])
		if !ok || unsupportedAppNames[app.Name] {
			continue
		}
		supported = append(supported, app)
	}
	return supported
}

// toOktaApp reads the common fields of the application in the list item,
// whichever sign-on mode schema it was decoded as.
func toOktaApp(item *okta.ListApplications200ResponseInner) (oktaApp, bool) {
	app, ok := item.GetActualInstance().(interface {
		GetId() string
		GetName() string
		GetSignOnMode() string
	})
	if !ok {
		return oktaApp{}, false
	}
	return oktaApp{ID: app.GetId(), Name: app.GetName(), SignOnMode: app.GetSignOnMode()}, true
}
