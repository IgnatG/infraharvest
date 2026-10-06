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

package gitlab

import (
	"context"

	"github.com/IgnatG/infraharvest/terraformutils"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

const gitLabDefaultURL = "https://gitlab.com/api/v4/"

type GitLabService struct { //nolint
	terraformutils.Service
}

// createClient returns a client for the configured GitLab instance. The client
// appends api/v4/ to a base URL that does not already end with it, so both
// https://gitlab.example.com and https://gitlab.example.com/api/v4 work.
func (g *GitLabService) createClient() (*gitlab.Client, error) {
	return newClient(g.GetArgs()["token"].(string), g.GetArgs()["base_url"].(string))
}

func newClient(token, baseURL string) (*gitlab.Client, error) {
	if baseURL == "" {
		baseURL = gitLabDefaultURL
	}
	return gitlab.NewClient(token, gitlab.WithBaseURL(baseURL))
}

// listAll pages through a GitLab list call and returns every item. It follows
// whichever pagination the response offers (offset or keyset).
func listAll[T any](ctx context.Context, list func(options ...gitlab.RequestOptionFunc) ([]T, *gitlab.Response, error)) ([]T, error) {
	return gitlab.ScanAndCollect(func(page gitlab.PaginationOptionFunc) ([]T, *gitlab.Response, error) {
		return list(gitlab.WithContext(ctx), page)
	})
}
