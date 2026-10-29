// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/smithy-go/encoding/cbor"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// Every lister of serviceTable keeps the listers' contract (see
// providertest.Contract) against a fake AWS: nothing listed from an empty
// account, a refusal classified by awsCodes as a failure, with its code,
// and a canceled context honoured. That each lists only the types its row
// declares is checked by the record tests, which list with listChecked
// (or listServiceChecked).
func TestListerContract(t *testing.T) {
	t.Parallel()
	for _, entry := range serviceTable {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			providertest.Contract{
				Declared: serviceTypes(awsCatalogue(), entry.name),
				List: func(ctx context.Context, _ *testing.T, cloud *providertest.Cloud) ([]harvest.Resource, error) {
					cfg := fakeConfig(nil)
					cfg.HTTPClient = cloud
					return entry.newLister().List(fakeRunIn(ctx, cfg), harvest.Settings{})
				},
				Empty:       awsAnswers(emptyAnswer),
				Denied:      awsAnswers(deniedAnswer),
				DeniedCodes: []string{"AccessDenied", "AccessDeniedException", "UnauthorizedOperation"},
				Codes:       awsCodes,
			}.Run(t)
		})
	}
}

// emptyAnswer answers call as an account with nothing in it does: an
// empty JSON or CBOR object, or no XML at all. The account still has an
// identity, and Security Hub answers that it isn't enabled.
func emptyAnswer(call apiCall) string {
	switch {
	case call.Op == "GetCallerIdentity":
		return recordCallerIdentity
	case call.Path == "/standards/get":
		return `{"__type":"InvalidAccessException","Message":"Account 123456789012 is ` + securityHubNotEnabled + `"}`
	}
	switch call.Protocol {
	case protocolJSON, protocolRESTJSON:
		return `{}`
	case protocolCBOR:
		return string(cbor.Encode(cbor.Map{}))
	}
	return ""
}

// deniedAnswer refuses call, in its protocol, as AWS refuses credentials
// without the permission.
func deniedAnswer(call apiCall) string {
	switch call.Protocol {
	case protocolQuery:
		return recordQueryError("https://iam.amazonaws.com/doc/2010-05-08/", "AccessDenied")
	case protocolEC2:
		return recordEC2Error()
	case protocolCBOR:
		return string(cbor.Encode(cbor.Map{"__type": cbor.String("AccessDeniedException"), "message": cbor.String("recorded failure")}))
	case protocolRESTXML:
		// S3 answers with a bare Error; Route 53 and CloudFront wrap it.
		if strings.HasPrefix(call.Host, "s3.") || strings.Contains(call.Host, ".s3.") {
			return `<Error><Code>AccessDenied</Code><Message>recorded failure</Message></Error>`
		}
		return `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>recorded failure</Message></Error><RequestId>r-1</RequestId></ErrorResponse>`
	}
	return recordJSONError("AccessDeniedException")
}

// listChecked lists with g, in ctx, as a record test does, and checks
// that g lists only the types its row of serviceTable declares.
func listChecked(ctx context.Context, t *testing.T, g harvest.Lister, s harvest.Settings) ([]harvest.Resource, error) {
	t.Helper()
	for _, entry := range serviceTable {
		if reflect.TypeOf(entry.newLister()) == reflect.TypeOf(g) {
			return listServiceChecked(ctx, t, g, entry.name, s)
		}
	}
	t.Errorf("%T is the lister of no service of serviceTable", g)
	return g.List(ctx, s)
}

// listServiceChecked lists with g, the lister of service, such as one
// Cloud Control lists or the guarded lister an import lists with, in
// ctx, and checks that g lists only the types service declares (see
// serviceTypes).
func listServiceChecked(ctx context.Context, t *testing.T, g harvest.Lister, service string, s harvest.Settings) ([]harvest.Resource, error) {
	t.Helper()
	resources, err := g.List(ctx, s)
	providertest.CheckDeclared(t, resources, serviceTypes(awsCatalogue(), service))
	return resources, err
}
