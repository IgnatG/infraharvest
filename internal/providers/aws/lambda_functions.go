// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// LambdaLister lists the region's Lambda functions with their
// permissions and event invoke configs, the event source mappings, and the
// layer versions (the `lambda` service).
type LambdaLister struct {
	Service
}

// lambdaResourcePolicy is the part of a function's resource policy the
// lister reads: the statement IDs, one aws_lambda_permission each.
type lambdaResourcePolicy struct {
	Statement []struct {
		Sid string
	}
}

// List records the functions, each followed by its permissions and
// event invoke configs, then the event source mappings, then every version
// of every layer. Any error fails the service, except that a function
// without a resource policy simply has no permissions.
func (g *LambdaLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := lambda.NewFromConfig(config)
	if err := g.addFunctions(svc); err != nil {
		return err
	}
	if err := g.addEventSourceMappings(svc); err != nil {
		return err
	}
	return g.addLayerVersions(svc)
}

// addFunctions records every function as an aws_lambda_function, imported
// by its ARN, named after its name, which it also records as the
// function_name attribute; each is followed by its permissions and event
// invoke configs.
func (g *LambdaLister) addFunctions(svc *lambda.Client) error {
	pages := lambda.NewListFunctionsPaginator(svc, &lambda.ListFunctionsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, function := range page.Functions {
			arn, name := aws.ToString(function.FunctionArn), aws.ToString(function.FunctionName)
			g.Resources = append(g.Resources, harvest.Record(arn, name, "aws_lambda_function", "aws",
				map[string]string{"function_name": name}))
			if err := g.addPermissions(svc, arn); err != nil {
				return err
			}
			if err := g.addEventInvokeConfigs(svc, arn, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// addPermissions records a statement of the resource policy of the function
// functionARN as an aws_lambda_permission, by Terraform's import ID
// "<function ARN>/<statement ID>" and named after its statement ID, with
// the function's ARN as the function_name attribute and the statement ID
// as statement_id. Statement IDs are only unique within a function, so the
// ID holds the function too.
func (g *LambdaLister) addPermissions(svc *lambda.Client, functionARN string) error {
	answer, err := svc.GetPolicy(g.Context(), &lambda.GetPolicyInput{FunctionName: aws.String(functionARN)})
	var noPolicy *types.ResourceNotFoundException
	if errors.As(err, &noPolicy) {
		return nil
	}
	if err != nil {
		return err
	}
	var policy lambdaResourcePolicy
	if err := json.Unmarshal([]byte(aws.ToString(answer.Policy)), &policy); err != nil {
		return fmt.Errorf("the resource policy of %s: %w", functionARN, err)
	}
	for _, statement := range policy.Statement {
		g.Resources = append(g.Resources, harvest.Record(functionARN+"/"+statement.Sid, statement.Sid, "aws_lambda_permission", "aws",
			map[string]string{"statement_id": statement.Sid, "function_name": functionARN}))
	}
	return nil
}

// addEventInvokeConfigs records the event invoke configs of the function
// functionName, whose ARN is functionARN, as
// aws_lambda_function_event_invoke_config, named feic_<the config's
// qualified ARN>, by Terraform's import ID: the function's name for the
// config of its unpublished version, $LATEST, or <name>:<qualifier> for
// that of a version or an alias.
func (g *LambdaLister) addEventInvokeConfigs(svc *lambda.Client, functionARN, functionName string) error {
	input := &lambda.ListFunctionEventInvokeConfigsInput{FunctionName: aws.String(functionName)}
	pages := lambda.NewListFunctionEventInvokeConfigsPaginator(svc, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, invokeConfig := range page.FunctionEventInvokeConfigs {
			configARN := aws.ToString(invokeConfig.FunctionArn)
			g.Resources = append(g.Resources, harvest.RecordBare(eventInvokeConfigID(functionARN, functionName, configARN),
				"feic_"+configARN, "aws_lambda_function_event_invoke_config", "aws"))
		}
	}
	return nil
}

// eventInvokeConfigID returns the import ID of the event invoke config
// whose qualified function ARN is configARN, of the function functionName
// whose ARN is functionARN: functionName for the $LATEST version or an
// unqualified ARN, <functionName>:<qualifier> otherwise.
func eventInvokeConfigID(functionARN, functionName, configARN string) string {
	qualifier, ok := strings.CutPrefix(configARN, functionARN+":")
	if !ok || qualifier == "" || qualifier == "$LATEST" {
		return functionName
	}
	return functionName + ":" + qualifier
}

// addEventSourceMappings records every event source mapping as an
// aws_lambda_event_source_mapping, imported and named by its UUID, with its
// source's ARN and its function's ARN as attributes.
func (g *LambdaLister) addEventSourceMappings(svc *lambda.Client) error {
	pages := lambda.NewListEventSourceMappingsPaginator(svc, &lambda.ListEventSourceMappingsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, mapping := range page.EventSourceMappings {
			id := aws.ToString(mapping.UUID)
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_lambda_event_source_mapping", "aws",
				map[string]string{
					"event_source_arn": aws.ToString(mapping.EventSourceArn),
					"function_name":    aws.ToString(mapping.FunctionArn),
				}))
		}
	}
	return nil
}

// addLayerVersions records every version of every layer as an
// aws_lambda_layer_version, imported and named by its ARN.
func (g *LambdaLister) addLayerVersions(svc *lambda.Client) error {
	layers := lambda.NewListLayersPaginator(svc, &lambda.ListLayersInput{}, stopOnDuplicateToken)
	for layers.HasMorePages() {
		page, err := layers.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, layer := range page.Layers {
			versions := lambda.NewListLayerVersionsPaginator(svc, &lambda.ListLayerVersionsInput{LayerName: layer.LayerName}, stopOnDuplicateToken)
			for versions.HasMorePages() {
				versionPage, err := versions.NextPage(g.Context())
				if err != nil {
					return err
				}
				for _, version := range versionPage.LayerVersions {
					arn := aws.ToString(version.LayerVersionArn)
					g.Resources = append(g.Resources, harvest.RecordBare(arn, arn, "aws_lambda_layer_version", "aws"))
				}
			}
		}
	}
	return nil
}
