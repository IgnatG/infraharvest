// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	retypes "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/types"
	"github.com/aws/smithy-go"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// explorerTypes maps the services Resource Explorer may rule out to the
// Resource Explorer types that cover everything their listers list: each
// Terraform type's own, or its parent's where it can't exist without one,
// such as a load balancer's listeners. A service is skipped in a region
// only if the index supports every one of its types and has none there.
//
// Left out on purpose: the global services; listers that also list what
// other accounts share with this one, such as subnets, VPCs, security
// groups and transit gateways, which the index doesn't have; listers of
// types the index lacks, such as launch configurations, API Gateway keys
// or SES templates; and listers of what AWS creates for the account, such
// as default parameter groups, Backup vaults or resolver rules, which it
// may not index. A service missing here is listed as before.
var explorerTypes = map[string][]string{
	"accessanalyzer": {"access-analyzer:analyzer"},
	"acm":            {"acm:certificate"},
	// Listeners, rules and attachments need a load balancer or target
	// group, and the index can report an ALB or NLB as the classic type.
	"alb": {"elasticloadbalancing:loadbalancer", "elasticloadbalancing:loadbalancer/app", "elasticloadbalancing:loadbalancer/gwy", "elasticloadbalancing:loadbalancer/net", "elasticloadbalancing:targetgroup"},
	// Routes, stages, integrations and the rest need their API.
	"api_gatewayv2": {"apigateway:apis", "apigateway:vpclinks"},
	"appconfig":     {"appconfig:application"},
	"appsync":       {"appsync:apis"},
	"batch":         {"batch:compute-environment", "batch:job-definition", "batch:job-queue"},
	"cloud9":        {"cloud9:environment"},
	// Stack set instances need their stack set.
	"cloudformation": {"cloudformation:stack", "cloudformation:stackset"},
	"codeartifact":   {"codeartifact:domain", "codeartifact:repository"},
	"codebuild":      {"codebuild:project"},
	"codedeploy":     {"codedeploy:application"},
	"codepipeline":   {"codepipeline:pipeline", "codepipeline:webhook"},
	// User pool clients need their pool.
	"cognito":          {"cognito-identity:identitypool", "cognito-idp:userpool"},
	"customer_gateway": {"ec2:customer-gateway"},
	"datapipeline":     {"datapipeline:pipeline"},
	"devicefarm":       {"devicefarm:project"},
	"dynamodb":         {"dynamodb:table"},
	// Volume attachments need their volume.
	"ebs":          {"ec2:volume"},
	"ec2_instance": {"ec2:instance"},
	// Repository and lifecycle policies need their repository.
	"ecr": {"ecr:repository"},
	"ecs": {"ecs:cluster", "ecs:service", "ecs:task-definition"},
	// Mount targets and file system policies need their file system.
	"efs": {"elasticfilesystem:access-point", "elasticfilesystem:file-system"},
	"eip": {"ec2:elastic-ip"},
	// Node groups, add-ons, access entries and the rest need their cluster.
	"eks":               {"eks:cluster"},
	"elastic_beanstalk": {"elasticbeanstalk:application", "elasticbeanstalk:environment"},
	"elb":               {"elasticloadbalancing:loadbalancer"},
	"es":                {"es:domain"},
	"firehose":          {"firehose:deliverystream"},
	"glue":              {"glue:crawler", "glue:database", "glue:job", "glue:table", "glue:trigger"},
	"guardduty":         {"guardduty:detector"},
	"iot":               {"iot:rolealias", "iot:rule", "iot:thing", "iot:thingtype"},
	"kinesis":           {"kinesis:stream"},
	// Permissions and invoke configurations need their function.
	"lambda":          {"lambda:event-source-mapping", "lambda:function", "lambda:layer/version"},
	"logs":            {"logs:log-group"},
	"media_package":   {"mediapackage:channels"},
	"media_store":     {"mediastore:container"},
	"mq":              {"mq:broker"},
	"msk":             {"kafka:cluster"},
	"nat":             {"ec2:natgateway"},
	"pipes":           {"pipes:pipe"},
	"placement_group": {"ec2:placement-group"},
	"resourcegroups":  {"resource-groups:group"},
	"secretsmanager":  {"secretsmanager:secret"},
	"sfn":             {"states:activity", "states:stateMachine"},
	"sqs":             {"sqs:queue"},
	"ssm":             {"ssm:parameter"},
	"synthetics":      {"synthetics:canary"},
	"vpn_connection":  {"ec2:vpn-connection"},
	"vpn_gateway":     {"ec2:vpn-gateway"},
	// Associations and logging configurations need their web ACL.
	"wafv2_regional": {"wafv2:ipset", "wafv2:regexpatternset", "wafv2:rulegroup", "wafv2:webacl"},
}

// explorerIndexMinAge is how long an index must have been in place before
// it rules anything out: a new one takes hours to find what a region has.
const explorerIndexMinAge = 24 * time.Hour

// explorerSearchLimit is the most resources a search returns, over all its
// pages.
const explorerSearchLimit = 1000

var (
	// resourceExplorer turns the pre-filter on (see UseResourceExplorer).
	resourceExplorer atomic.Bool
	explorerMu       sync.Mutex
	// explorerRegions holds what Resource Explorer says about each account
	// and region, read once for all of its services.
	explorerRegions = map[configKey]*explorerRegion{}
	// explorerNow is the clock an index's age is measured by.
	explorerNow = time.Now
)

// UseResourceExplorer makes the services listed after it skip the regions
// where AWS Resource Explorer's index has none of their resources (see
// explorerTypes). It is off by default: the index can lag behind recent
// changes by hours, and a resource it hasn't found yet would be left out.
func UseResourceExplorer(on bool) {
	resourceExplorer.Store(on)
}

// explorerSkip returns why the service needn't list its region: Resource
// Explorer is on and its index shows the service has nothing there. It
// returns "" otherwise, and whenever the index can't tell.
func (s *guardedLister) explorerSkip() string {
	if !resourceExplorer.Load() {
		return ""
	}
	name := s.lister.Name()
	types, ok := explorerTypes[name]
	if !ok {
		return ""
	}
	svc := &Service{}
	svc.Configure(harvest.ListerConfig{Settings: s.Settings()})
	svc.SetContext(s.Context())
	return explorerFor(svc).skip(svc.Context(), name, types)
}

// explorerRegion is what Resource Explorer says about one account and
// region: the types of the resources the default view shows there.
type explorerRegion struct {
	once sync.Once
	// unusable says why the index can't rule anything out; "" if it can.
	unusable string
	region   string
	// client and viewARN search the region: in the region itself, or in
	// the region of the aggregator index its index replicates to.
	client  *resourceexplorer2.Client
	viewARN string
	// supported are the types Resource Explorer indexes.
	supported map[string]bool
	// present are the types of the resources a search of the region
	// found: all of them if complete, else of the first
	// explorerSearchLimit.
	present  map[string]bool
	found    int
	complete bool
	// typeSearch is whether a search for given types finds one the region
	// has, checked once before relying on such searches.
	typeSearchOnce sync.Once
	typeSearch     bool
}

// explorerFor returns what Resource Explorer says about the account and
// region svc lists, read the first time a service asks. If it can't rule
// anything out, it logs why, once.
func explorerFor(svc *Service) *explorerRegion {
	key := svc.cacheKey()
	explorerMu.Lock()
	x, ok := explorerRegions[key]
	if !ok {
		x = &explorerRegion{region: key.region}
		explorerRegions[key] = x
	}
	explorerMu.Unlock()
	x.once.Do(func() {
		x.unusable = x.load(svc)
		where := x.region
		if where == ProfileRegion {
			where = "the default region"
		}
		if x.unusable != "" {
			log.Printf("aws: Resource Explorer can't rule out services in %s: %s; listing every service", where, x.unusable)
			return
		}
		shows := fmt.Sprintf("%d resources of %d types", x.found, len(x.present))
		if !x.complete {
			shows = fmt.Sprintf("more than %d resources, so services with none among them are searched for one by one", explorerSearchLimit)
		}
		log.Printf("aws: Resource Explorer: the default view %s shows %s in %s; services it has none of there aren't listed", x.viewARN, shows, where)
	})
	return x
}

// load finds the region's index and default view, the types Resource
// Explorer supports, and the types of the region's resources. It returns
// why they can't rule anything out, if they can't.
func (x *explorerRegion) load(svc *Service) string {
	ctx := svc.Context()
	cfg, err := svc.sdkConfig()
	if err != nil {
		return err.Error()
	}
	switch x.region {
	case GlobalPseudoRegion:
		return "global services aren't filtered"
	case ProfileRegion:
		x.region = cfg.Region
	}
	x.client = resourceexplorer2.NewFromConfig(cfg)
	// The region's own index: without one, no aggregator has its resources
	// either.
	index, reason := activeIndex(ctx, x.client, false)
	if reason != "" {
		return reason
	}
	if x.viewARN, reason = defaultView(ctx, x.client); reason != "" {
		return reason
	}
	if x.viewARN == "" {
		// The console's setup makes a default view only in the region of
		// the aggregator index, which has this region's resources too.
		if reason := x.useAggregator(ctx, cfg, index); reason != "" {
			return reason
		}
	}
	if reason := checkView(ctx, x.client, x.viewARN); reason != "" {
		return reason
	}
	if x.supported, err = supportedTypes(ctx, x.client); err != nil {
		return explorerError("ListSupportedResourceTypes", err)
	}
	if err := x.searchRegion(ctx); err != nil {
		return explorerError("Search", err)
	}
	return ""
}

// activeIndex returns the index of the client's region, or why it can't
// rule anything out: there is none, it is still being built, or it is too
// new to have found everything. An aggregator also counts from when it
// last changed, such as when it became the aggregator.
func activeIndex(ctx context.Context, client *resourceexplorer2.Client, aggregator bool) (*resourceexplorer2.GetIndexOutput, string) {
	index, err := client.GetIndex(ctx, &resourceexplorer2.GetIndexInput{})
	if apiErrorCode(err) == "ResourceNotFoundException" || (err == nil && aws.ToString(index.Arn) == "") {
		return nil, "no index"
	}
	if err != nil {
		return nil, explorerError("GetIndex", err)
	}
	if index.State != retypes.IndexStateActive {
		return nil, fmt.Sprintf("the index is %s", index.State)
	}
	since := aws.ToTime(index.CreatedAt)
	if updated := aws.ToTime(index.LastUpdatedAt); aggregator && updated.After(since) {
		since = updated
	}
	if explorerNow().Sub(since) < explorerIndexMinAge {
		return nil, fmt.Sprintf("the index dates from %s, less than a day ago, and may not have found every resource yet", since.UTC().Format(time.RFC3339))
	}
	return index, ""
}

// useAggregator searches the region through the default view of the
// aggregator index its index replicates to, if there is one.
func (x *explorerRegion) useAggregator(ctx context.Context, cfg aws.Config, local *resourceexplorer2.GetIndexOutput) string {
	if local.Type != retypes.IndexTypeLocal || len(local.ReplicatingTo) == 0 {
		return "no default view"
	}
	cfg = cfg.Copy()
	cfg.Region = local.ReplicatingTo[0]
	client := resourceexplorer2.NewFromConfig(cfg)
	index, reason := activeIndex(ctx, client, true)
	if reason == "" && (index.Type != retypes.IndexTypeAggregator || !slices.Contains(index.ReplicatingFrom, x.region)) {
		reason = "that index doesn't aggregate this region's"
	}
	if reason == "" {
		x.viewARN, reason = defaultView(ctx, client)
		if reason == "" && x.viewARN == "" {
			reason = "no default view there either"
		}
	}
	if reason != "" {
		return fmt.Sprintf("no default view, and in %s, where the index replicates to: %s", cfg.Region, reason)
	}
	x.client = client
	return ""
}

// defaultView returns the ARN of the default view of the client's region,
// "" if it has none, or why it can't be read.
func defaultView(ctx context.Context, client *resourceexplorer2.Client) (string, string) {
	out, err := client.GetDefaultView(ctx, &resourceexplorer2.GetDefaultViewInput{})
	if apiErrorCode(err) == "ResourceNotFoundException" {
		return "", ""
	}
	if err != nil {
		return "", explorerError("GetDefaultView", err)
	}
	return aws.ToString(out.ViewArn), ""
}

// checkView returns why the view can't rule resources out, if it can't: it
// filters them, or covers something other than its own account.
func checkView(ctx context.Context, client *resourceexplorer2.Client, viewARN string) string {
	out, err := client.GetView(ctx, &resourceexplorer2.GetViewInput{ViewArn: aws.String(viewARN)})
	if err != nil {
		return explorerError("GetView", err)
	}
	if out.View == nil {
		return "GetView returned no view"
	}
	if f := out.View.Filters; f != nil && strings.TrimSpace(aws.ToString(f.FilterString)) != "" {
		return fmt.Sprintf("the default view %s shows only %q", viewARN, aws.ToString(f.FilterString))
	}
	if scope := aws.ToString(out.View.Scope); scope != "" && scope != accountRoot(viewARN) {
		return fmt.Sprintf("the default view %s covers %s, not its account", viewARN, scope)
	}
	return ""
}

// accountRoot returns the root ARN of the account an ARN belongs to:
// arn:aws:iam::123456789012:root.
func accountRoot(arn string) string {
	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 {
		return ""
	}
	return "arn:" + parts[1] + ":iam::" + parts[4] + ":root"
}

func supportedTypes(ctx context.Context, client *resourceexplorer2.Client) (map[string]bool, error) {
	supported := map[string]bool{}
	p := resourceexplorer2.NewListSupportedResourceTypesPaginator(client, &resourceexplorer2.ListSupportedResourceTypesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range page.ResourceTypes {
			supported[aws.ToString(t.ResourceType)] = true
		}
	}
	return supported, nil
}

// searchRegion records the types of the region's resources, from as many
// as a search returns: all of them, if there are fewer than
// explorerSearchLimit.
func (x *explorerRegion) searchRegion(ctx context.Context) error {
	x.present = map[string]bool{}
	x.complete = true
	p := resourceexplorer2.NewSearchPaginator(x.client, &resourceexplorer2.SearchInput{
		QueryString: aws.String("region:" + x.region),
		ViewArn:     aws.String(x.viewARN),
		MaxResults:  aws.Int32(explorerSearchLimit),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, r := range page.Resources {
			x.present[aws.ToString(r.ResourceType)] = true
		}
		x.found += len(page.Resources)
		if c := page.Count; c != nil && !aws.ToBool(c.Complete) {
			x.complete = false
		}
	}
	if x.found >= explorerSearchLimit {
		x.complete = false
	}
	return nil
}

// skip returns why a service of types needn't list the region: the index
// supports each of them and the region has none; "" otherwise.
func (x *explorerRegion) skip(ctx context.Context, service string, types []string) string {
	if x.unusable != "" {
		return ""
	}
	for _, t := range types {
		if !x.supported[t] {
			log.Printf("aws: %s: Resource Explorer doesn't index %s; listing it", service, t)
			return ""
		}
		if x.present[t] {
			return ""
		}
	}
	if !x.complete {
		// The region has more than one search returns: search for the
		// service's types alone.
		x.typeSearchOnce.Do(func() { x.typeSearch = x.typeSearchWorks(ctx) })
		if !x.typeSearch {
			return ""
		}
		found, err := x.hasAny(ctx, types)
		if err != nil {
			log.Printf("aws: %s: Resource Explorer can't rule it out in %s: %s; listing it", service, x.region, explorerError("Search", err))
			return ""
		}
		if found {
			return ""
		}
	}
	return fmt.Sprintf("Resource Explorer's index has no %s in %s", strings.Join(types, ", "), x.region)
}

// typeSearchWorks reports whether a search for two types finds one of
// them that the region has, as the searches for a service's types assume.
func (x *explorerRegion) typeSearchWorks(ctx context.Context) bool {
	var known, other string
	for t := range x.present {
		if x.supported[t] && (known == "" || t < known) {
			known = t
		}
	}
	for t := range x.supported {
		if t != known && (other == "" || t < other) {
			other = t
		}
	}
	if known == "" || other == "" {
		return false
	}
	found, err := x.hasAny(ctx, []string{other, known})
	if err != nil || !found {
		log.Printf("aws: Resource Explorer: a search in %s for %s or %s doesn't find the %s it has; listing every service not among its first %d resources", x.region, other, known, known, explorerSearchLimit)
		return false
	}
	return true
}

// hasAny reports whether the region has a resource of one of types.
func (x *explorerRegion) hasAny(ctx context.Context, types []string) (bool, error) {
	out, err := x.client.Search(ctx, &resourceexplorer2.SearchInput{
		QueryString: aws.String("region:" + x.region + " resourcetype:" + strings.Join(types, ",")),
		ViewArn:     aws.String(x.viewARN),
		MaxResults:  aws.Int32(1),
	})
	if err != nil {
		return false, err
	}
	found := len(out.Resources) > 0 || aws.ToString(out.NextToken) != ""
	if c := out.Count; c != nil && aws.ToInt64(c.TotalResources) > 0 {
		found = true
	}
	return found, nil
}

// explorerError describes a failed Resource Explorer call; one the role
// isn't allowed names the action.
func explorerError(op string, err error) string {
	if code := apiErrorCode(err); code == "AccessDeniedException" || code == "UnauthorizedException" {
		return fmt.Sprintf("not allowed resource-explorer-2:%s (%v)", op, err)
	}
	return fmt.Sprintf("%s: %v", op, err)
}

// apiErrorCode returns the error code of an AWS API error, "" for any
// other error.
func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}
