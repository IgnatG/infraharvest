// Copyright 2019 The Terraformer Authors.
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

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

type CloudflareService struct { //nolint
	terraformutils.Service
}

func (s *CloudflareService) initializeAPI() (*cloudflare.Client, error) {
	opts, err := authOptions(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	return cloudflare.NewClient(opts...), nil
}

// accountID is the account the account-level listers list, from
// CLOUDFLARE_ACCOUNT_ID.
func (s *CloudflareService) accountID() string {
	return os.Getenv("CLOUDFLARE_ACCOUNT_ID")
}

// authOptions authenticates with CLOUDFLARE_API_TOKEN, else with
// CLOUDFLARE_API_KEY and CLOUDFLARE_EMAIL. The SDK client also reads the
// other variables from the environment, so the headers of the scheme not
// chosen are removed: requests carry one set of credentials, as before.
func authOptions(getenv func(string) string) ([]option.RequestOption, error) {
	apiKey := getenv("CLOUDFLARE_API_KEY")
	apiEmail := getenv("CLOUDFLARE_EMAIL")
	apiToken := getenv("CLOUDFLARE_API_TOKEN")

	if apiToken == "" && (apiEmail == "" || apiKey == "") {
		return nil, errors.New("Either CLOUDFLARE_API_TOKEN or CLOUDFLARE_API_KEY/CLOUDFLARE_EMAIL environment variables must be set") //nolint:revive,staticcheck // message users know
	}

	if apiToken != "" {
		return []option.RequestOption{
			option.WithAPIToken(apiToken),
			option.WithHeaderDel("X-Auth-Key"),
			option.WithHeaderDel("X-Auth-Email"),
			option.WithHeaderDel("X-Auth-User-Service-Key"),
		}, nil
	}

	return []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithAPIEmail(apiEmail),
		option.WithHeaderDel("Authorization"),
		option.WithHeaderDel("X-Auth-User-Service-Key"),
	}, nil
}

// collect reads every page of pager.
func collect[T any](pager *pagination.V4PagePaginationArrayAutoPager[T]) ([]T, error) {
	var items []T
	for pager.Next() {
		items = append(items, pager.Current())
	}
	return items, pager.Err()
}

// listZones lists every zone the credentials can see.
func listZones(ctx context.Context, client *cloudflare.Client) ([]zones.Zone, error) {
	return collect(client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{}))
}

// retired reports whether err is the 410 Gone Cloudflare answers for an API
// it has retired (Firewall Rules, Filters, the previous Rate Limiting API).
// A retired API has nothing left to list, so the lister lists nothing
// instead of failing the whole service; it logs that it skipped it.
func retired(err error, what, zoneName string) bool {
	var apiErr *cloudflare.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusGone {
		log.Printf("cloudflare: zone %s: Cloudflare has retired the %s API (410 Gone), skipping", zoneName, what)
		return true
	}
	return false
}
