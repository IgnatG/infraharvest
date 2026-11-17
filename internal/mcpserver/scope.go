// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/sentence"
)

// Scope is what to list: a provider command, its services, and where to
// list them, in the provider's own terms. Each field but Provider sets the
// flag its flag tag names (see flagArgs). A field with a scope tag is for
// the providers whose profiles declare its flag alone (see
// harvest.Scope.Flags), so that a provider's parameters come with it:
// the tools reject it for the others (see checkScopeProviders), the
// tools' descriptions say which provider takes which (see scopeHelp),
// and their schemas have it only where a provider compiled in takes it,
// its description naming them for {providers} (see scopeSchema), as the
// provider's description names the provider commands compiled in.
type Scope struct {
	Provider string   `json:"provider" jsonschema:"the provider command"`
	Services []string `json:"services,omitempty" flag:"services" jsonschema:"services to list, such as vpc, s3 and iam; with cfn_stacks, those of the stacks' resource types when left out"`
	// QueryTypes are the types the query service lists through Terraform's
	// list resources, which every provider has.
	QueryTypes []string `json:"query_types,omitempty" flag:"query-types" jsonschema:"resource types for the query service to list through Terraform's own list resources (terraform query, Terraform 1.14 or later), such as aws_flow_log, for types no other service lists; with query in services, and not with engine tofu"`
	// Regions is for aws and google.
	Regions []string `json:"regions,omitempty" flag:"regions" scope:"provider" jsonschema:"{providers} only: regions to list, such as eu-west-2 for aws or global and europe-west1 for google (default: the profile's region for aws, global for google)"`
	// Profile and Accounts are for aws.
	Profile  string   `json:"profile,omitempty" flag:"profile" scope:"provider" jsonschema:"{providers} only: the AWS profile whose credentials to use"`
	Accounts []string `json:"accounts,omitempty" flag:"accounts" scope:"provider" jsonschema:"{providers} only: accounts to list, each through the role infraharvest-readonly in it"`
	// CloudFormationStacks and CloudFormationIgnore are for aws: the
	// stacks whose resources alone to import, and those of their resources
	// Terraform can't import that they may be deleted without.
	CloudFormationStacks []string `json:"cfn_stacks,omitempty" flag:"cfn-stack,each" scope:"provider" jsonschema:"{providers} only: CloudFormation stacks, by name or stack ID (ARN), whose resources and nested stacks' alone to import; the report says whether each can be deleted once they are in Terraform"`
	CloudFormationIgnore []string `json:"cfn_ignore,omitempty" flag:"cfn-ignore,each" scope:"provider" jsonschema:"{providers} only, with cfn_stacks: resources of the stacks Terraform can't import, such as custom resources, by logical ID or as <stack>/<logical ID>, which the stacks may be deleted without"`
	// Projects, Organization and Folders are for google, which needs at
	// least one of them.
	Projects     []string `json:"projects,omitempty" flag:"projects" scope:"provider" jsonschema:"{providers} only: projects to list; google needs projects, organization or folders"`
	Organization string   `json:"organization,omitempty" flag:"organization" scope:"provider" jsonschema:"{providers} only: an organization ID, to list every active project of it, in its folders too"`
	Folders      []string `json:"folders,omitempty" flag:"folders" scope:"provider" jsonschema:"{providers} only: folder IDs, to list every active project of them, in their subfolders too"`
	// Subscriptions, ManagementGroup and ResourceGroup are for azure.
	Subscriptions   []string `json:"subscriptions,omitempty" flag:"subscriptions" scope:"provider" jsonschema:"{providers} only: subscription IDs to list (default: ARM_SUBSCRIPTION_ID's)"`
	ManagementGroup string   `json:"management_group,omitempty" flag:"management-group" scope:"provider" jsonschema:"{providers} only: a management group ID, to list every enabled subscription under it, in its child groups too"`
	ResourceGroup   string   `json:"resource_group,omitempty" flag:"resource-group" scope:"provider" jsonschema:"{providers} only: the resource group to list (default: the whole subscription)"`
	// Azapi is for azure.
	Azapi bool `json:"azapi,omitempty" flag:"azapi" scope:"provider" jsonschema:"{providers} only: import what Resource Graph lists that azurerm has no resource type for as azapi_resource, through the Azure/azapi provider, instead of leaving it out"`
}

// scopeHelp says which provider takes which of Scope's fields, for the
// tools' descriptions, from their profiles' flags (see scopeTakers), and
// which of them a provider's command needs one of (see
// harvest.Scope.Needs).
func scopeHelp(compiled Providers) string {
	scope := reflect.TypeFor[Scope]()
	var takes, needs []string
	for _, profile := range compiled.Profiles {
		provider := profile.Command
		var fields, needed []string
		required := profile.Scope.Needs
		for i := range scope.NumField() {
			field := scope.Field(i)
			if !slices.Contains(compiled.scopeTakers(field), provider) {
				continue
			}
			fields = append(fields, jsonName(field))
			if slices.Contains(required, flagName(field)) {
				needed = append(needed, jsonName(field))
			}
		}
		takes = append(takes, provider+" takes "+sentence.List(fields, "and"))
		if len(needed) > 0 {
			needs = append(needs, " The "+provider+" command needs "+sentence.List(needed, "or")+".")
		}
	}
	return "Where to list depends on the provider: " + strings.Join(takes, "; ") + "." + strings.Join(needs, "")
}

// scopeTakers returns the providers that field, one of Scope's or of a
// tool's input, is for: for a field with a scope tag, those whose
// profiles declare its flag (see harvest.Scope.Flags), by command, maybe
// none; for any other, nil, as every provider takes it.
func (p Providers) scopeTakers(field reflect.StructField) []string {
	if _, ok := field.Tag.Lookup("scope"); !ok {
		return nil
	}
	takers := []string{}
	for _, profile := range p.Profiles {
		if slices.Contains(profile.Scope.Flags, flagName(field)) {
			takers = append(takers, profile.Command)
		}
	}
	return takers
}

// flagName returns the name of the flag field sets, from its flag tag.
func flagName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("flag"), ",")
	return name
}

// jsonName returns the name an agent sends field by.
func jsonName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

// scopeSchema returns the input schema of a tool whose input In embeds
// Scope, as the SDK would infer it, for the providers compiled: with
// their commands after the provider's description, the provider command:
// aws, azure or google; and with each field of Scope that is for some
// providers alone (see scopeTakers) only if one of them is compiled, its
// description naming those that are where it has {providers}. It panics
// where the schema can't be inferred, as the SDK does.
func scopeSchema[In any](compiled Providers) *jsonschema.Schema {
	schema, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("mcpserver: the input schema of %T: %v", *new(In), err))
	}
	provider := schema.Properties["provider"]
	provider.Description += ": " + sentence.List(compiled.names(), "or")
	scope := reflect.TypeFor[Scope]()
	for i := range scope.NumField() {
		field := scope.Field(i)
		takers := compiled.scopeTakers(field)
		if takers == nil {
			continue
		}
		name := jsonName(field)
		if len(takers) == 0 {
			delete(schema.Properties, name)
			schema.PropertyOrder = slices.DeleteFunc(schema.PropertyOrder, func(p string) bool { return p == name })
			continue
		}
		property := schema.Properties[name]
		property.Description = strings.ReplaceAll(property.Description, "{providers}", sentence.List(takers, "and"))
	}
	return schema
}

// checkScopeProviders rejects each field of s that is set and is for
// other providers of compiled than s's (see scopeTakers), rather than
// leave it to a flag the command may have with another meaning: aws has
// --organization too, which lists the whole organisation.
func (p Providers) checkScopeProviders(s Scope) error {
	v := reflect.ValueOf(s)
	var errs []error
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		takers := p.scopeTakers(field)
		if takers == nil || !isSet(value) || slices.Contains(takers, s.Provider) {
			continue
		}
		if len(takers) == 0 {
			errs = append(errs, fmt.Errorf("%s doesn't take %s, which no provider of this infraharvest takes", s.Provider, jsonName(field)))
			continue
		}
		errs = append(errs, fmt.Errorf("%s doesn't take %s, which is for %s only", s.Provider, jsonName(field), strings.Join(takers, " and ")))
	}
	return errors.Join(errs...)
}

// isSet says whether value, a field of a tool's input, is set: a list
// with no values isn't.
func isSet(value reflect.Value) bool {
	if value.Kind() == reflect.Slice {
		return value.Len() > 0
	}
	return !value.IsZero()
}

// commandArgs returns the arguments of command on scope's provider, with
// the flags of in, a tool's input that has scope in it (see flagArgs). A
// field for other providers is rejected (see checkScopeProviders); what
// the provider's command makes of the rest, such as a Google scope
// without a project, is for the command to check: discover returns the
// error it runs into, and import checks them with the command's checks
// before the user is asked (see Checker).
func (p Providers) commandArgs(command string, scope Scope, in any) ([]string, error) {
	// The provider is the one argument that isn't a --flag=value: one the
	// binary doesn't have would be taken for something else, such as a
	// flag.
	if err := p.Check(scope.Provider); err != nil {
		return nil, err
	}
	if err := checkScopeNames(scope); err != nil {
		return nil, err
	}
	if err := p.checkScopeProviders(scope); err != nil {
		return nil, err
	}
	flags, err := flagArgs(in)
	if err != nil {
		return nil, err
	}
	return append([]string{command, scope.Provider}, flags...), nil
}

// flagArgs returns the flags that in, a tool's input, sets: for each field
// with a flag tag, in this one place, the flag that tag names, with the
// field's value, or none when the field isn't set. A list goes into one
// flag, comma-separated, so that a value of it with a comma, which the
// command would take for two, is rejected; or with ",each" in the tag, as
// a flag that takes one value each time such as --cfn-stack, into a flag
// per value. An empty value in a list is rejected. Every value is part of
// a --flag=value argument, so none can become a flag of its own; one with
// a control character, such as a newline, which could make the
// confirmation the user is shown say something else, is rejected. The
// fields of an embedded struct come in its place.
func flagArgs(in any) ([]string, error) {
	var args []string
	v := reflect.ValueOf(in)
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		if field.Anonymous {
			embedded, err := flagArgs(value.Interface())
			if err != nil {
				return nil, err
			}
			args = append(args, embedded...)
			continue
		}
		tag, ok := field.Tag.Lookup("flag")
		if !ok {
			continue
		}
		name, mode, _ := strings.Cut(tag, ",")
		var values []string
		switch value.Kind() {
		case reflect.Bool:
			if value.Bool() {
				args = append(args, "--"+name)
			}
			continue
		case reflect.Int:
			if value.Int() != 0 {
				values = []string{strconv.FormatInt(value.Int(), 10)}
			}
		case reflect.String:
			if value.String() != "" {
				values = []string{value.String()}
			}
		case reflect.Slice:
			values = value.Interface().([]string)
			for _, v := range values {
				if v == "" {
					return nil, fmt.Errorf("%s has an empty value", jsonName(field))
				}
				if mode != "each" && strings.Contains(v, ",") {
					return nil, fmt.Errorf("%s %q has a comma", jsonName(field), v)
				}
			}
			if len(values) > 0 && mode != "each" {
				values = []string{strings.Join(values, ",")}
			}
		}
		for _, value := range values {
			if strings.ContainsFunc(value, unicode.IsControl) {
				return nil, fmt.Errorf("%s %q has a control character", jsonName(field), value)
			}
			args = append(args, "--"+name+"="+value)
		}
	}
	return args, nil
}

// namePattern is what a service or a region is named like: vpc, s3,
// resource_graph, eu-west-2, global, or * for all.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_*.-]+$`)

// typePattern is what a resource type is named like: aws_flow_log.
var typePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkScopeNames rejects a service or region that isn't named like one,
// and a query type that isn't named like a resource type, which the
// confirmation the user is shown names.
func checkScopeNames(s Scope) error {
	for _, v := range append(slices.Clone(s.Services), s.Regions...) {
		if !namePattern.MatchString(v) {
			return fmt.Errorf("%q isn't a service or a region", v)
		}
	}
	for _, v := range s.QueryTypes {
		if !typePattern.MatchString(v) {
			return fmt.Errorf("query_types: %q isn't a resource type", v)
		}
	}
	return nil
}

// checkQueryEngine refuses query types with engine, the run's, as the
// command does: the query service lists through terraform query, which
// OpenTofu doesn't have. The command would refuse it too, but discover
// runs without checking first, and a configuration file can set either.
func checkQueryEngine(s Scope, engine string) error {
	if len(s.QueryTypes) > 0 && engine == "tofu" {
		return fmt.Errorf("query_types: the %s service lists through Terraform's list resources (terraform query), which OpenTofu doesn't have: use engine terraform", harvest.QueryService)
	}
	return nil
}

// shellJoin joins args into a command line a POSIX shell reads back as
// args: each argument with anything but letters, digits and -_.,:/=@+ in
// it is single-quoted, so a path with a space or a ; stays one argument.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_.,:/=@+-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// scopeText says what an import reads, for the user to confirm: what
// profile, the profile of scope's provider, describes of the flags scope
// sets (see harvest.Scope.Describe), the provider's accounts,
// subscriptions or projects and where in them, in the provider's own
// terms; the services, which, when scope names none, the stacks'
// resources do; and the types the query service lists through
// Terraform's list resources. scope is the run's: the tool's input, with what its
// configuration file sets of the rest (see toolConfig.scope), and each
// field set is one its provider takes.
func scopeText(s Scope, profile harvest.Profile) (string, error) {
	if profile.Scope.Describe == nil {
		return "", fmt.Errorf("can't say what provider %q reads", s.Provider)
	}
	text := profile.Scope.Describe(scopeFlags(s))
	if len(s.Services) == 0 {
		text += " (the services of the stacks' resources)"
	} else {
		text += " (" + strings.Join(s.Services, ", ") + ")"
	}
	if len(s.QueryTypes) > 0 {
		text += ", listing " + strings.Join(s.QueryTypes, ", ") + " through Terraform's list resources"
	}
	return text, nil
}

// scopeFlags returns the flags of s that are set, by the names their flag
// tags give them, with their values, as a provider's profile describes
// them (see harvest.Scope.Describe): a flag that takes no value has true.
func scopeFlags(s Scope) map[string][]string {
	flags := map[string][]string{}
	v := reflect.ValueOf(s)
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		tag, ok := field.Tag.Lookup("flag")
		if !ok || !isSet(value) {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		switch value.Kind() {
		case reflect.Bool:
			flags[name] = []string{"true"}
		case reflect.String:
			flags[name] = []string{value.String()}
		case reflect.Slice:
			flags[name] = value.Interface().([]string)
		}
	}
	return flags
}
