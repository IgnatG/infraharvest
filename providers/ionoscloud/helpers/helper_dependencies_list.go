package helpers

import (
	"context"
	"log"

	ionoscloud "github.com/ionos-cloud/sdk-go/v6"
)

func GetAllDatacenters(ctx context.Context, client ionoscloud.APIClient) ([]ionoscloud.Datacenter, error) {
	datacenters, _, err := client.DataCentersApi.DatacentersGet(ctx).Depth(1).Execute()
	if err != nil {
		return nil, err
	}
	if datacenters.Items == nil {
		log.Printf("[WARNING] expected a response containing datacenters but received 'nil' instead.")
		return nil, nil
	}
	return *datacenters.Items, nil
}
