// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// cloudControlType is a resource type listed through the Cloud Control
// API, which lists any CloudFormation type with one call: its
// CloudFormation type, and the Terraform type it imports as.
type cloudControlType struct {
	CloudFormation string
	// Identifier is the one property of the CloudFormation type's primary
	// identifier, whose value Cloud Control returns as each resource's
	// identifier, and which is also the Terraform type's import ID: a type
	// whose identifier isn't, or has several properties, needs the ID
	// built. The CloudFormation schemas are checked against it (see
	// TestCloudControlIdentifiersMatchSchemas); the Terraform documentation
	// was, when the type was added.
	Identifier string
	Terraform  string
}

// cloudControlServices are the services the built-in catalogue lists
// through Cloud Control (see cloudControlServicesOf). They are worked out
// once, as the built-in catalogue doesn't change; callers must not modify
// the answer.
var cloudControlServices = sync.OnceValue(func() map[string][]cloudControlType {
	return cloudControlServicesOf(awsCatalogue())
})

// cloudControlServicesOf returns the services cat lists through Cloud
// Control, by name, each a group of related types the service listers
// don't cover: the catalogue's types with a cloud_control entry, by
// service, sorted by Terraform type. What AWS creates for the account,
// such as the default event bus, the catalogue's skip rules leave out.
func cloudControlServicesOf(cat *catalogue.Catalogue) map[string][]cloudControlType {
	services := map[string][]cloudControlType{}
	for _, typ := range cat.Types() {
		e, _ := cat.Entry(typ)
		if e.CloudControl == nil {
			continue
		}
		services[e.Service] = append(services[e.Service], cloudControlType{
			CloudFormation: e.CloudControl.Type,
			Identifier:     e.CloudControl.Identifier,
			Terraform:      typ,
		})
	}
	return services
}

// cloudControl returns the services the catalogue of the import lists
// through Cloud Control (see SetCatalogue): with a --catalogue overlay,
// its cloud_control entries add types, and services, to the built-in
// ones.
func (p *Provider) cloudControl() map[string][]cloudControlType {
	if p.cat == nil {
		return cloudControlServices()
	}
	return cloudControlServicesOf(p.cat)
}

// CloudControlLister lists a service of cloudControlServices.
type CloudControlLister struct {
	Service
	types []cloudControlType
}

func newCloudControlLister(service string) *CloudControlLister {
	return &CloudControlLister{types: cloudControlServices()[service]}
}

// List lists every type of the service, each on its own: a type that fails,
// such as one an SCP denies, doesn't stop the others. When some types fail,
// their errors, each naming its type, are returned together as a partial
// error (see harvest.Partial), so what the others listed is imported; when
// every type fails, as the service's failure.
func (g *CloudControlLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := cloudcontrol.NewFromConfig(config)
	var errs []error
	for _, t := range g.types {
		if err := g.list(g.Context(), client, t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.CloudFormation, err))
		}
	}
	if len(errs) > 0 && len(errs) == len(g.types) {
		return errors.Join(errs...)
	}
	return harvest.Partial(errors.Join(errs...))
}

// list adds the resources of one type, if the region has the type. A type
// Cloud Control can't list there, which has no list handler, is logged and
// lists nothing.
func (g *CloudControlLister) list(ctx context.Context, client cloudcontrol.ListResourcesAPIClient, t cloudControlType) error {
	p := cloudcontrol.NewListResourcesPaginator(client, &cloudcontrol.ListResourcesInput{TypeName: aws.String(t.CloudFormation)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		var notFound *cctypes.TypeNotFoundException
		if errors.As(err, &notFound) {
			// Not every region offers every type.
			return nil
		}
		var unsupported *cctypes.UnsupportedActionException
		if errors.As(err, &unsupported) {
			log.Printf("aws: cloud control can't list %s here; not listing it: %v", t.CloudFormation, err)
			return nil
		}
		if err != nil {
			return err
		}
		for _, r := range page.ResourceDescriptions {
			id := aws.ToString(r.Identifier)
			if id == "" {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(id, cloudControlName(id), t.Terraform, "aws"))
		}
	}
	return nil
}

// cloudControlName names a resource after its identifier: an ARN's
// resource name, or the identifier itself.
func cloudControlName(id string) string {
	if strings.HasPrefix(id, "arn:") {
		return id[strings.LastIndexAny(id, "/:")+1:]
	}
	return id
}
