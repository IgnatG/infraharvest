// Copyright 2018 The Terraformer Authors.
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

package github

import (
	"net/http"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"
)

const githubDefaultURL = "https://api.github.com/"

type GithubService struct { //nolint
	terraformutils.Service
}

// createClient returns a GitHub client for base_url (github.com when it is
// empty or the default), authenticated as the GitHub App installation when
// app_id, installation_id and pem are all set, and with the token otherwise.
func (g *GithubService) createClient() (*github.Client, error) {
	baseURL, _ := g.GetArgs()["base_url"].(string)
	appID, _ := g.GetArgs()["app_id"].(int64)
	installationID, _ := g.GetArgs()["installation_id"].(int64)
	pem, _ := g.GetArgs()["pem"].(string)
	token, _ := g.GetArgs()["token"].(string)
	return newClient(baseURL, token, appID, installationID, pem)
}

func newClient(baseURL, token string, appID, installationID int64, pem string) (*github.Client, error) {
	enterprise := baseURL != "" && baseURL != githubDefaultURL
	var opts []github.ClientOptionsFunc
	if enterprise {
		opts = append(opts, github.WithEnterpriseURLs(baseURL, baseURL))
	}

	var installation *ghinstallation.Transport
	if appID != 0 && installationID != 0 && pem != "" {
		itr, err := ghinstallation.New(http.DefaultTransport, appID, installationID, []byte(pem))
		if err != nil {
			return nil, err
		}
		installation = itr
		opts = append(opts, github.WithTransport(itr))
	} else if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}

	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	if installation != nil && enterprise {
		// Installation tokens come from the same API as everything else.
		installation.BaseURL = strings.TrimSuffix(client.BaseURL(), "/")
	}
	return client, nil
}
