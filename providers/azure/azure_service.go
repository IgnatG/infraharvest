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

package azure

import (
	"context"
	"log"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type AzureService struct { //nolint
	terraformutils.Service
}

// getClientArgs returns what every Azure SDK client constructor takes, plus
// the --resource-group scope ("" for the whole subscription).
func (az *AzureService) getClientArgs() (subscriptionID string, resourceGroup string, credential azcore.TokenCredential, options *arm.ClientOptions) {
	subscriptionID, _ = az.Args["subscription_id"].(string)
	resourceGroup, _ = az.Args["resource_group"].(string)
	credential, _ = az.Args["credential"].(azcore.TokenCredential)
	options, _ = az.Args["client_options"].(*arm.ClientOptions)
	return subscriptionID, resourceGroup, credential, options
}

// listAll walks every page of pager and returns the items of all pages.
// It stops at the first page that fails and returns the items read so far
// with the error.
func listAll[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T) ([]*T, error) {
	return walkPages(ctx, pager, items, false)
}

// listAllLenient is listAll for listers that only fail when the first page
// fails: an error on a later page is logged and the items read so far are
// returned without an error.
func listAllLenient[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T) ([]*T, error) {
	return walkPages(ctx, pager, items, true)
}

func walkPages[P any, T any](ctx context.Context, pager *runtime.Pager[P], items func(P) []*T, lenient bool) ([]*T, error) {
	var all []*T
	for first := true; pager.More(); first = false {
		page, err := pager.NextPage(ctx)
		if err != nil {
			if lenient && !first {
				log.Println(err)
				return all, nil
			}
			return all, err
		}
		for _, item := range items(page) {
			if item != nil {
				all = append(all, item)
			}
		}
	}
	return all, nil
}

func (az *AzureService) AppendSimpleResource(id string, resourceName string, resourceType string) {
	newResource := terraformutils.NewSimpleResource(id, resourceName, resourceType, az.ProviderName)
	az.Resources = append(az.Resources, newResource)
}

func (az *AzureService) AppendSimpleResourceWithDuplicateCheck(id string, resourceName string, resourceType string) {
	tferexist, _ := az.DuplicateCheck(id, resourceName, resourceType)
	if !tferexist {
		resourceName = resourceName + "_" + id
	}
	newResource := terraformutils.NewSimpleResource(id, resourceName, resourceType, az.ProviderName)
	az.Resources = append(az.Resources, newResource)
}

// This method checks if same resource name(tfer) exists with
// same id
func (az *AzureService) DuplicateCheck(id string, resourceName string, resourceType string) (bool, bool) {
	var tferexist, idexist bool
	tferName := terraformutils.TfSanitize(resourceName)
	for _, resource := range az.Resources {
		if tferName == resource.ResourceName {
			if id == resource.InstanceState.ID {
				tferexist = true
				idexist = true
			} else {
				tferexist = true
				idexist = false
			}
		}
	}
	return tferexist, idexist
}

func (az *AzureService) appendSimpleAssociation(id string, linkedResourceName string, resourceName *string, resourceType string, attributes map[string]string) {
	var resourceName2 string
	if resourceName != nil {
		resourceName2 = *resourceName
	} else {
		resourceName0 := strings.ReplaceAll(resourceType, "azurerm_", "")
		resourceName1 := resourceName0[strings.IndexByte(resourceName0, '_'):]
		resourceName2 = linkedResourceName + resourceName1
	}
	newResource := terraformutils.NewResource(
		id, resourceName2, resourceType, az.ProviderName, attributes)

	az.Resources = append(az.Resources, newResource)
}
