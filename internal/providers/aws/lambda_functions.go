// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
// event invoke configs, then the event source mappings, then the layer
// versions: each layer's latest, and those the functions use. Any error
// fails the service, except that a function without a resource policy
// simply has no permissions.
func (g *LambdaLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *LambdaLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := lambda.NewFromConfig(config)
	used := map[string]bool{}
	if err := g.addFunctions(svc, used); err != nil {
		return err
	}
	if err := g.addEventSourceMappings(svc); err != nil {
		return err
	}
	return g.addLayerVersions(svc, used)
}

// addFunctions records every function as an aws_lambda_function, imported
// by its ARN, named after its name, which it also records as the
// function_name attribute; each is followed by its permissions and event
// invoke configs. The ARNs of the layer versions the functions use, which
// the list answers already, are added to used. The functions of a page are
// read resourceParallel at a time, as each takes two calls of its
// own, and recorded in the page's order.
func (g *LambdaLister) addFunctions(svc *lambda.Client, used map[string]bool) error {
	pages := lambda.NewListFunctionsPaginator(svc, &lambda.ListFunctionsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		parts := make([][]harvest.Resource, len(page.Functions))
		errs := make([]error, len(page.Functions))
		inParallel(len(page.Functions), resourceParallel, func(i int) {
			function := page.Functions[i]
			parts[i], errs[i] = g.functionParts(svc, aws.ToString(function.FunctionArn), aws.ToString(function.FunctionName))
		})
		for i, function := range page.Functions {
			arn, name := aws.ToString(function.FunctionArn), aws.ToString(function.FunctionName)
			for _, layer := range function.Layers {
				used[aws.ToString(layer.Arn)] = true
			}
			g.Resources = append(g.Resources, harvest.Record(arn, name, "aws_lambda_function", "aws",
				map[string]string{"function_name": name}))
			if errs[i] != nil {
				return errs[i]
			}
			g.Resources = append(g.Resources, parts[i]...)
		}
	}
	return nil
}

// functionParts returns the permissions, then the event invoke configs, of
// the function functionName, whose ARN is functionARN.
// Each takes a call of its own, skipped when the import doesn't keep its
// type.
func (g *LambdaLister) functionParts(svc *lambda.Client, functionARN, functionName string) ([]harvest.Resource, error) {
	var permissions []harvest.Resource
	if g.Wants("aws_lambda_permission") {
		var err error
		if permissions, err = g.permissions(svc, functionARN); err != nil {
			return nil, err
		}
	}
	if !g.Wants("aws_lambda_function_event_invoke_config") {
		return permissions, nil
	}
	configs, err := g.eventInvokeConfigs(svc, functionARN, functionName)
	return append(permissions, configs...), err
}

// permissions returns a statement of the resource policy of the function
// functionARN as an aws_lambda_permission, by Terraform's import ID
// "<function ARN>/<statement ID>" and named after its statement ID, with
// the function's ARN as the function_name attribute and the statement ID
// as statement_id. Statement IDs are only unique within a function, so the
// ID holds the function too.
func (g *LambdaLister) permissions(svc *lambda.Client, functionARN string) ([]harvest.Resource, error) {
	answer, err := svc.GetPolicy(g.Context(), &lambda.GetPolicyInput{FunctionName: aws.String(functionARN)})
	var noPolicy *types.ResourceNotFoundException
	if errors.As(err, &noPolicy) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var policy lambdaResourcePolicy
	if err := json.Unmarshal([]byte(aws.ToString(answer.Policy)), &policy); err != nil {
		return nil, fmt.Errorf("the resource policy of %s: %w", functionARN, err)
	}
	var resources []harvest.Resource
	for _, statement := range policy.Statement {
		resources = append(resources, harvest.Record(functionARN+"/"+statement.Sid, statement.Sid, "aws_lambda_permission", "aws",
			map[string]string{"statement_id": statement.Sid, "function_name": functionARN}))
	}
	return resources, nil
}

// eventInvokeConfigs returns the event invoke configs of the function
// functionName, whose ARN is functionARN, as
// aws_lambda_function_event_invoke_config, named feic_<the config's
// qualified ARN>, by Terraform's import ID: the function's name for the
// config of its unpublished version, $LATEST, or <name>:<qualifier> for
// that of a version or an alias.
func (g *LambdaLister) eventInvokeConfigs(svc *lambda.Client, functionARN, functionName string) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	input := &lambda.ListFunctionEventInvokeConfigsInput{FunctionName: aws.String(functionName)}
	pages := lambda.NewListFunctionEventInvokeConfigsPaginator(svc, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return nil, err
		}
		for _, invokeConfig := range page.FunctionEventInvokeConfigs {
			configARN := aws.ToString(invokeConfig.FunctionArn)
			resources = append(resources, harvest.RecordBare(eventInvokeConfigID(functionARN, functionName, configARN),
				"feic_"+configARN, "aws_lambda_function_event_invoke_config", "aws"))
		}
	}
	return resources, nil
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

// addLayerVersions records layer versions as aws_lambda_layer_version,
// imported by their ARN and named after the layer and the version: "deps
// 2". Of each layer, it records the latest version, which ListLayers
// answers already, followed by the older versions in used, by version
// number: a function can go on using a version after a newer one is
// published. Older versions nothing uses are history, as Terraform manages
// a layer's code by its latest version, so they are left out, and no layer
// takes a call of its own.
func (g *LambdaLister) addLayerVersions(svc *lambda.Client, used map[string]bool) error {
	layers := lambda.NewListLayersPaginator(svc, &lambda.ListLayersInput{}, stopOnDuplicateToken)
	for layers.HasMorePages() {
		page, err := layers.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, layer := range page.Layers {
			latest := layer.LatestMatchingVersion
			if latest == nil {
				continue
			}
			layerName := aws.ToString(layer.LayerName)
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(latest.LayerVersionArn),
				layerName+" "+strconv.FormatInt(latest.Version, 10), "aws_lambda_layer_version", "aws"))
			for _, version := range olderLayerVersions(aws.ToString(layer.LayerArn), latest.Version, used) {
				g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(layer.LayerArn)+":"+strconv.FormatInt(version, 10),
					layerName+" "+strconv.FormatInt(version, 10), "aws_lambda_layer_version", "aws"))
			}
		}
	}
	return nil
}

// olderLayerVersions returns the numbers of the versions of the layer
// layerARN in used that are older than latest, sorted. A version ARN is
// the layer's ARN, a colon and the version number.
func olderLayerVersions(layerARN string, latest int64, used map[string]bool) []int64 {
	var versions []int64
	for arn := range used {
		suffix, ok := strings.CutPrefix(arn, layerARN+":")
		if !ok {
			continue
		}
		if version, err := strconv.ParseInt(suffix, 10, 64); err == nil && version < latest {
			versions = append(versions, version)
		}
	}
	slices.Sort(versions)
	return versions
}
