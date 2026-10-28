// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
	"go.yaml.in/yaml/v3"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
)

// nestedStackType is the CloudFormation type of a nested stack, whose
// resources are those of the stack it names.
const nestedStackType = "AWS::CloudFormation::Stack"

// cloudFormationOnlyTypes are the CloudFormation types of resources that
// exist only in CloudFormation: there is nothing to import, and deleting
// the stack deletes nothing else with them.
var cloudFormationOnlyTypes = map[string]string{
	"AWS::CDK::Metadata":                       "the CDK's metadata, which exists only in CloudFormation",
	"AWS::CloudFormation::WaitCondition":       "a wait condition, which exists only in CloudFormation",
	"AWS::CloudFormation::WaitConditionHandle": "a wait condition handle, which exists only in CloudFormation",
}

// StackScope is what --cfn-stack scopes an AWS import to: the stacks it
// names and, recursively, the stacks nested in them, each with its
// resources and its template, read once before anything is listed (see
// ResolveStacks). The provider of each Import call is given it (see
// Provider.SetStackScope): the call imports the resources of those stacks
// only, as far as it lists them.
type StackScope struct {
	stacks []*scopedStack
}

// scopedStack is a stack of a StackScope.
type scopedStack struct {
	name, id, region, status string
	// parent is the ID of the stack it is nested in, "" for one
	// --cfn-stack names, and root that of the top-level stack it is nested
	// in, as DescribeStacks gives it.
	parent, root string
	// deleted says CloudFormation deleted it: its stack ID still reads it,
	// for 90 days, and the resources it retained are what is left.
	deleted bool
	// unreadable says why a nested stack couldn't be read, as when its
	// deleted parent deleted it longer ago than CloudFormation keeps it,
	// or the role may not describe it: what it holds is unknown.
	unreadable string
	warnings   []string
	template   *stackTemplate
	resources  []*scopedResource

	// parameters are the values of its parameters, by name, for a stack
	// --cfn-stack names (DescribeStacks gives them): as the stack was
	// given them, so an SSM parameter type's is the SSM parameter's name,
	// and a NoEcho one's masked; resolved are the values the SSM
	// parameter types resolved to. A nested stack has none: they would
	// take a DescribeStacks call each.
	parameters, resolved map[string]string
}

// scopedResource is a resource of a scopedStack.
type scopedResource struct {
	logicalID, cfnType, physicalID, status string
	// outcome is cfn.StackNested or cfn.StackOnlyInCloudFormation
	// for what is never imported but doesn't keep the stack from being
	// deleted, and notImportable why Terraform can't import it, if it
	// can't.
	outcome, notImportable string
	// retain says what keeps it from outliving its stack (see
	// stackTemplate.retainProblems).
	retain []string
	// mustRemove says it must be taken out of the stack before the stack
	// is deleted, and acknowledged that --cfn-ignore accepts that
	// Terraform can't import it (see customResourceRule).
	mustRemove, acknowledged bool
}

// ResolveStacks reads the stacks refs names, by name, which finds a live
// stack in region (ProfileRegion for the profile's), or by stack ID (its
// ARN), which finds it in the ARN's region, deleted too, for 90 days after.
// It reads each one's resources and template, after transforms, and those
// of the stacks nested in it, recursively: DescribeStacks once for each of
// refs and for each nested stack, for its status, then ListStackResources
// and GetTemplate once for each stack. cat tells the Terraform types of
// their resources (see stackTypes) and the stacks a service manages. It
// changes nothing.
func ResolveStacks(ctx context.Context, profile, roleARN, region string, refs []string, cat *catalogue.Catalogue) (*StackScope, error) {
	service := &Service{}
	service.SetSettings(serviceSettings{region: region, profile: profile, roleARN: roleARN})
	service.SetContext(ctx)
	config, err := service.sdkConfig()
	if err != nil {
		return nil, err
	}
	scope := &StackScope{}
	types := stackTypes(cat)
	for _, ref := range refs {
		regional := config.Copy()
		if r := arnRegion(ref); r != "" {
			regional.Region = r
		}
		client := cloudformation.NewFromConfig(regional)
		out, err := client.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(ref)})
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" {
				return nil, fmt.Errorf("--cfn-stack %s: no such stack in %s (a deleted stack is found by its stack ID, its ARN, for 90 days): %w", ref, regional.Region, err)
			}
			return nil, fmt.Errorf("--cfn-stack %s: %w", ref, err)
		}
		if len(out.Stacks) == 0 {
			return nil, fmt.Errorf("--cfn-stack %s: no such stack in %s", ref, regional.Region)
		}
		s := out.Stacks[0]
		id := aws.ToString(s.StackId)
		if slices.ContainsFunc(scope.stacks, func(have *scopedStack) bool { return have.id == id }) {
			continue
		}
		stack := &scopedStack{
			name:    aws.ToString(s.StackName),
			id:      id,
			region:  arnRegion(id),
			status:  string(s.StackStatus),
			deleted: s.StackStatus == cftypes.StackStatusDeleteComplete,
		}
		for _, p := range s.Parameters {
			if stack.parameters == nil {
				stack.parameters = map[string]string{}
			}
			key := aws.ToString(p.ParameterKey)
			stack.parameters[key] = aws.ToString(p.ParameterValue)
			if resolved := aws.ToString(p.ResolvedValue); resolved != "" {
				if stack.resolved == nil {
					stack.resolved = map[string]string{}
				}
				stack.resolved[key] = resolved
			}
		}
		if reason := cat.Skipped("aws_cloudformation_stack", id, map[string]string{"name": stack.name}); reason != "" {
			stack.warnings = append(stack.warnings, "it is "+reason+": that service may create its resources again, or fail, once they are gone from it")
		}
		if parent := aws.ToString(s.ParentId); parent != "" {
			stack.parent = parent
			stack.root = aws.ToString(s.RootId)
			stack.warnings = append(stack.warnings, "it is nested in "+parent+", which deletes it with itself: name the root stack to delete them together")
		}
		if err := scope.read(ctx, client, stack, types); err != nil {
			return nil, err
		}
	}
	return scope, nil
}

// read adds stack to the scope, with its resources and its template, and
// then each stack nested in it, but for one the scope has already, as when
// --cfn-stack names a nested stack before its parent. A nested stack has
// no status yet: one DescribeStacks call gives it, whether a deleted parent
// deleted it or retained it (DELETE_SKIPPED), which leaves it live. One
// that call fails for is added as unreadable, with the reason, and the
// rest is read: the report keeps it, and each stack it is nested in, from
// being safe to delete. So is one whose template or resources can't be
// read, as where the role may describe it but not get its template.
func (s *StackScope) read(ctx context.Context, client *cloudformation.Client, stack *scopedStack, types map[string][]string) error {
	if slices.ContainsFunc(s.stacks, func(have *scopedStack) bool { return have.id == stack.id }) {
		return nil
	}
	// unreadable records a nested stack that couldn't be read, by err, and
	// reports whether it is one: a stack --cfn-stack names, or a request
	// the run's end interrupted, fails the run instead.
	unreadable := func(err error) bool {
		if stack.parent == "" || ctx.Err() != nil {
			return false
		}
		stack.unreadable = err.Error()
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			stack.unreadable = apiErr.ErrorCode() + ": " + apiErr.ErrorMessage()
		}
		stack.resources = nil
		return true
	}
	if stack.status == "" {
		out, err := client.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stack.id)})
		if err != nil {
			if !unreadable(err) {
				return fmt.Errorf("CloudFormation stack %s: %w", stack.name, err)
			}
			s.stacks = append(s.stacks, stack)
			return nil
		}
		if len(out.Stacks) > 0 {
			stack.status = string(out.Stacks[0].StackStatus)
			stack.deleted = out.Stacks[0].StackStatus == cftypes.StackStatusDeleteComplete
			stack.root = aws.ToString(out.Stacks[0].RootId)
		}
	}
	s.stacks = append(s.stacks, stack)
	template, err := client.GetTemplate(ctx, &cloudformation.GetTemplateInput{StackName: aws.String(stack.id), TemplateStage: cftypes.TemplateStageProcessed})
	if err != nil {
		if unreadable(err) {
			return nil
		}
		return fmt.Errorf("CloudFormation stack %s: template: %w", stack.name, err)
	}
	if stack.template, err = parseTemplate(aws.ToString(template.TemplateBody)); err != nil {
		return fmt.Errorf("CloudFormation stack %s: %w", stack.name, err)
	}
	var nested []*scopedStack
	pages := cloudformation.NewListStackResourcesPaginator(client, &cloudformation.ListStackResourcesInput{StackName: aws.String(stack.id)}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			if unreadable(err) {
				return nil
			}
			return fmt.Errorf("CloudFormation stack %s: resources: %w", stack.name, err)
		}
		for _, summary := range page.StackResourceSummaries {
			r := &scopedResource{
				logicalID:  aws.ToString(summary.LogicalResourceId),
				cfnType:    aws.ToString(summary.ResourceType),
				physicalID: aws.ToString(summary.PhysicalResourceId),
				status:     string(summary.ResourceStatus),
			}
			if r.physicalID == "" {
				// Never created, as when its creation failed.
				continue
			}
			stack.resources = append(stack.resources, r)
			gone := summary.ResourceStatus == cftypes.ResourceStatusDeleteComplete
			switch {
			case r.cfnType == nestedStackType:
				r.outcome = cfn.StackNested
				r.notImportable = "a nested stack, whose resources are listed with it"
				// A live stack lists the nested stacks it deleted, as one that
				// rolled back does: those have nothing left. A deleted one
				// lists those it deleted with it, which may have retained
				// resources, and those it retained, which are live.
				if !gone || stack.deleted {
					nested = append(nested, &scopedStack{name: stackName(r.physicalID), id: r.physicalID, region: arnRegion(r.physicalID), parent: stack.id})
				}
				continue
			case cloudFormationOnlyTypes[r.cfnType] != "":
				r.outcome = cfn.StackOnlyInCloudFormation
				r.notImportable = cloudFormationOnlyTypes[r.cfnType]
				continue
			case isCustomResource(r.cfnType) && types[r.cfnType] == nil:
				// What it is may depend on its properties, which can name
				// resources listed after it (see customResourceAdvice).
				r.notImportable = genericCustomResource
			case types[r.cfnType] == nil:
				r.notImportable = "infraharvest knows no Terraform type for " + r.cfnType
			}
			if !stack.deleted {
				r.retain = stack.template.retainProblems(r.logicalID, r.cfnType)
			}
		}
	}
	for _, r := range stack.resources {
		if isCustomResource(r.cfnType) && r.notImportable != "" {
			r.notImportable, r.mustRemove = customResourceAdvice(stack, r)
		}
	}
	for _, n := range nested {
		if err := s.read(ctx, client, n, types); err != nil {
			return err
		}
	}
	return nil
}

// arnRegion returns the region of an ARN, such as a stack ID, "" for what
// isn't one.
func arnRegion(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	return parts[3]
}

// stackName returns the name in a stack ID,
// arn:aws:cloudformation:<region>:<account>:stack/<name>/<uuid>.
func stackName(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) < 2 {
		return id
	}
	return parts[1]
}

// Regions returns the regions of the stacks, sorted.
func (s *StackScope) Regions() []string {
	var regions []string
	for _, stack := range s.stacks {
		if !slices.Contains(regions, stack.region) {
			regions = append(regions, stack.region)
		}
	}
	sort.Strings(regions)
	return regions
}

// Services returns the services that list the Terraform types of the
// stacks' resources, by cat, sorted: all an import of the stacks needs to
// list.
func (s *StackScope) Services(cat *catalogue.Catalogue) []string {
	types := stackTypes(cat)
	var services []string
	for _, stack := range s.stacks {
		for _, r := range stack.resources {
			if r.outcome != "" || r.notImportable != "" {
				continue
			}
			for _, typ := range types[r.cfnType] {
				if e, ok := cat.Entry(typ); ok && e.Service != "" && !slices.Contains(services, e.Service) {
					services = append(services, e.Service)
				}
			}
		}
	}
	sort.Strings(services)
	return services
}

// Summary returns a line for the log about each stack: how many resources
// it has, how many Terraform can't import, and how many aren't retained.
func (s *StackScope) Summary() []string {
	var lines []string
	for _, stack := range s.stacks {
		var notImportable, notRetained []string
		for _, r := range stack.resources {
			if r.outcome == "" && r.notImportable != "" {
				note := r.logicalID + " (" + r.cfnType + ")"
				switch {
				case r.mustRemove:
					note += ", which must be taken out of the stack before it is deleted"
				case r.acknowledged:
					note += ", acknowledged"
				}
				notImportable = append(notImportable, note)
			}
			if len(r.retain) > 0 {
				notRetained = append(notRetained, r.logicalID)
			}
		}
		if stack.unreadable != "" {
			lines = append(lines, fmt.Sprintf("CloudFormation stack %s (%s), nested in %s: couldn't be read, so what it holds is unknown, and neither it nor a stack it is nested in is safe to delete: %s", stack.name, stack.region, stack.parent, stack.unreadable))
			continue
		}
		line := fmt.Sprintf("CloudFormation stack %s (%s, %s): %d resources", stack.name, stack.region, stack.status, len(stack.resources))
		if stack.deleted {
			line += "; deleted already, so what it retained is imported"
		}
		if len(notImportable) > 0 {
			line += "; can't be imported: " + strings.Join(notImportable, ", ")
		}
		if len(notRetained) > 0 {
			line += fmt.Sprintf("; %d lack DeletionPolicy and UpdateReplacePolicy Retain, so deleting the stack would delete them: %s", len(notRetained), strings.Join(notRetained, ", "))
		}
		for _, w := range stack.warnings {
			line += "; warning: " + w
		}
		lines = append(lines, line)
	}
	return lines
}

// names returns the names of the stacks --cfn-stack names, without those
// nested in them.
func (s *StackScope) names() []string {
	var names []string
	for _, stack := range s.stacks {
		if stack.parent == "" || !slices.ContainsFunc(s.stacks, func(p *scopedStack) bool { return p.id == stack.parent }) {
			names = append(names, stack.name)
		}
	}
	return names
}

// nesting returns, for a nested stack, the ID of its root stack, the
// logical ID of its parent's resource that creates it, and that
// resource's TemplateURL where it is a string rather than a function's
// value, as the report needs them to update it through its root (see
// cfn.Stack.Root); "" for what the scope doesn't know,
// as of a parent --cfn-stack doesn't reach. It makes no call.
func (s *StackScope) nesting(stack *scopedStack) (root, logicalID, templateURL string) {
	byID := func(id string) *scopedStack {
		i := slices.IndexFunc(s.stacks, func(have *scopedStack) bool { return have.id == id })
		if i < 0 {
			return nil
		}
		return s.stacks[i]
	}
	if stack.parent == "" {
		return "", "", ""
	}
	parent := byID(stack.parent)
	root = stack.root
	if root == "" {
		top := stack
		for p := parent; p != nil; p = byID(p.parent) {
			top = p
		}
		root = top.root
		if top.parent == "" {
			root = top.id
		}
	}
	if parent == nil {
		return root, "", ""
	}
	for _, r := range parent.resources {
		if r.cfnType == nestedStackType && r.physicalID == stack.id {
			logicalID = r.logicalID
		}
	}
	if property := mappingValue(mappingValue(parent.template.resource(logicalID), "Properties"), "TemplateURL"); property != nil && property.Kind == yaml.ScalarNode && property.ShortTag() == "!!str" {
		templateURL = property.Value
	}
	return root, logicalID, templateURL
}

// reasonOutsideStacks is why an import scoped to stacks leaves out what
// isn't theirs.
func reasonOutsideStacks(names []string) string {
	what := "CloudFormation stack " + names[0]
	if len(names) > 1 {
		what = "CloudFormation stacks " + strings.Join(names, ", ")
	}
	return report.OutsideScopePrefix + "not a resource of " + what + " (--cfn-stack)"
}

// SetStackScope scopes the provider's import to the stacks of scope (see
// ImportScope).
func (p *Provider) SetStackScope(scope *StackScope) {
	p.stackScope = scope
}

// stackMatch is a listed resource found to be a resource of a stack.
type stackMatch struct {
	resource harvest.Resource
	stack    *scopedStack
	of       *scopedResource
	// exact says the stack resource's type lists as the resource's type
	// itself, rather than as one that shares its ID, such as a bucket's
	// policy, which shares the bucket's.
	exact bool
}

// ImportScope returns, for an import scoped to stacks (see SetStackScope),
// the resources the stacks hold, which the import takes whatever the
// default selection says, and the others, which it leaves out whatever a
// selection file says, by "<type> <id>"; and it keeps the stacks, each
// resource with what it is among resources, for CloudFormationStacks. Only the stacks of the provider's
// region count, but for the global services, which every region's stacks
// can create resources of. It makes no call: it matches the physical IDs
// of the stacks' resources with what is listed, by their types (see
// stackTypes), as ExcludedByDefault does, but never with a resource of a
// type it doesn't know. A resource that matches only as one of the types
// that share a resource's ID, such as a bucket policy, which has its
// bucket's, goes to the stack's resource of its own type whose properties
// name that resource, if there is one: the policy's physical ID isn't the
// bucket's name.
func (p *Provider) ImportScope(_ context.Context, resources []harvest.Resource) (*harvest.ImportScope, error) {
	if p.stackScope == nil {
		return nil, nil
	}
	cat := p.catalogue()
	types := stackTypes(cat)
	type owner struct {
		stack *scopedStack
		of    *scopedResource
	}
	byID := map[string][]owner{}
	for _, stack := range p.stackScope.stacks {
		if p.region != GlobalPseudoRegion && stack.region != p.region {
			continue
		}
		for _, r := range stack.resources {
			if r.outcome == "" && r.notImportable == "" && !strings.HasPrefix(r.status, "DELETE_COMPLETE") {
				byID[r.physicalID] = append(byID[r.physicalID], owner{stack, r})
			}
		}
	}
	scope := &harvest.ImportScope{Inside: map[string]bool{}, Outside: map[string]string{}}
	outside := reasonOutsideStacks(p.stackScope.names())
	var matches []stackMatch
	for _, r := range resources {
		key := r.Address.Type + " " + r.Observed.ID
		ids := []string{r.Observed.ID}
		if id, importable := importID(cat, r); importable && id != r.Observed.ID {
			ids = append(ids, id)
		}
		var best *stackMatch
		for _, id := range ids {
			for _, o := range byID[id] {
				covers, _ := stackTypeCovers(cat, types, o.of.cfnType, r.Address.Type)
				if !covers {
					continue
				}
				exact := slices.Contains(types[o.of.cfnType], r.Address.Type)
				if best == nil || (exact && !best.exact) {
					best = &stackMatch{resource: r, stack: o.stack, of: o.of, exact: exact}
				}
			}
		}
		if best == nil {
			scope.Outside[key] = outside
			continue
		}
		scope.Inside[key] = true
		matches = append(matches, *best)
	}
	reassignShared(matches, types)
	byResource := map[*scopedResource][]cfn.Match{}
	for _, m := range matches {
		id, ok := importID(cat, m.resource)
		if !ok {
			id = m.resource.Observed.ID
		}
		byResource[m.of] = append(byResource[m.of], cfn.Match{Type: m.resource.Address.Type, ID: id})
	}
	for _, stack := range p.stackScope.stacks {
		stack.holdIn(byResource)
	}
	p.scopedStacks = nil
	for _, stack := range p.stackScope.stacks {
		reported := stack.report(byResource)
		reported.Root, reported.NestedAs, reported.TemplateURL = p.stackScope.nesting(stack)
		p.scopedStacks = append(p.scopedStacks, reported)
	}
	return scope, nil
}

// CloudFormationStacks returns the stacks the import is scoped to, for
// the report, each resource with the listed resources it is, as
// ImportScope matched them; nil before ImportScope.
func (p *Provider) CloudFormationStacks() []cfn.Stack {
	return p.scopedStacks
}

// reassignShared gives each match that is only of a type sharing its
// stack resource's ID (see ImportScope) to the resource of the same stack
// of a CloudFormation type that lists as its type exactly and that no
// listed resource matched, whose properties name the stack resource, such
// as a bucket policy's Bucket.
func reassignShared(matches []stackMatch, types map[string][]string) {
	matched := map[*scopedResource]bool{}
	for _, m := range matches {
		matched[m.of] = true
	}
	for i := range matches {
		m := &matches[i]
		if m.exact {
			continue
		}
		for _, r := range m.stack.resources {
			if matched[r] || r.notImportable != "" || !slices.Contains(types[r.cfnType], m.resource.Address.Type) {
				continue
			}
			if m.stack.template.refersTo(r.logicalID, m.of.logicalID, m.of.physicalID) {
				m.of, m.exact = r, true
				matched[r] = true
				break
			}
		}
	}
}

// report returns the stack as the report has it, each resource with the
// listed resources byResource says it is.
func (s *scopedStack) report(byResource map[*scopedResource][]cfn.Match) cfn.Stack {
	stack := cfn.Stack{Name: s.name, ID: s.id, Region: s.region, Status: s.status, Parent: s.parent, Deleted: s.deleted, Unreadable: s.unreadable, Warnings: s.warnings}
	stack.SetParameters(s.parameters, s.resolved)
	var unretained []string
	for _, r := range s.resources {
		stack.Resources = append(stack.Resources, cfn.Resource{
			LogicalID:     r.logicalID,
			Type:          r.cfnType,
			PhysicalID:    r.physicalID,
			Status:        r.status,
			NotImportable: r.notImportable,
			MustRemove:    r.mustRemove,
			Acknowledged:  r.acknowledged,
			Retain:        r.retain,
			Matches:       byResource[r],
			Outcome:       r.outcome,
		})
		if len(r.retain) > 0 {
			unretained = append(unretained, r.logicalID)
		}
	}
	if s.template != nil {
		ext := "yaml"
		if s.template.json {
			ext = "json"
		}
		retain := ""
		if len(unretained) > 0 {
			// A template that parsed writes back; should it not, the
			// report still says what lacks Retain.
			retain, _ = s.template.withRetain(unretained)
		}
		stack.SetTemplates(s.template.body, retain, ext)
	}
	return stack
}

// heldTypes are the CloudFormation types of resources that Terraform holds
// as an argument of another resource, which the listers record with it,
// by type: the Terraform type of that resource, and the property that
// names it, by Ref. A queue's policy is its aws_sqs_queue's policy, and a
// topic's its aws_sns_topic's.
var heldTypes = map[string]struct{ terraform, property string }{
	"AWS::SQS::QueuePolicy": {"aws_sqs_queue", "Queues"},
	"AWS::SNS::TopicPolicy": {"aws_sns_topic", "Topics"},
}

// holdIn gives each resource of the stack that another holds (see
// heldTypes), and that matched nothing listed itself, the matches of the
// resources it names that are of the holding type: it is imported where
// they are.
func (s *scopedStack) holdIn(byResource map[*scopedResource][]cfn.Match) {
	for _, r := range s.resources {
		held, ok := heldTypes[r.cfnType]
		if !ok || len(byResource[r]) > 0 {
			continue
		}
		refs := s.refs(r.logicalID, held.property)
		for _, other := range s.resources {
			if !slices.Contains(refs, other.logicalID) {
				continue
			}
			for _, m := range byResource[other] {
				if m.Type == held.terraform {
					byResource[r] = append(byResource[r], m)
				}
			}
		}
	}
}

// refs returns the logical IDs the property of the stack's resource
// logicalID names by Ref, in either form, anywhere in its value.
func (s *scopedStack) refs(logicalID, property string) []string {
	var refs []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch {
		case n == nil:
		case n.Kind == yaml.ScalarNode && n.Tag == "!Ref":
			refs = append(refs, n.Value)
		case n.Kind == yaml.MappingNode:
			if r := mappingValue(n, "Ref"); r != nil && r.Kind == yaml.ScalarNode {
				refs = append(refs, r.Value)
				return
			}
			fallthrough
		default:
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(mappingValue(mappingValue(s.template.resource(logicalID), "Properties"), property))
	return refs
}
