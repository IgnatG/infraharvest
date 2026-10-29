// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"log"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/magodo/armid"
	"golang.org/x/sync/errgroup"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// diagnosticSettingService lists diagnostic settings. It is opt-in: *
// leaves it out, since Resource Graph doesn't index diagnostic settings
// and listing them takes a request per resource (see
// DiagnosticSettingLister).
const diagnosticSettingService = "diagnostic_setting"

// diagnosticSettingType is the azurerm type of a diagnostic setting.
const diagnosticSettingType = "azurerm_monitor_diagnostic_setting"

// storageAccountType is the Resource Manager type of a storage account.
const storageAccountType = "Microsoft.Storage/storageAccounts"

// storageServices are the services every storage account has, as the
// last segments of their IDs under the account's: each has diagnostic
// settings of its own, and the account's logs are only theirs. Resource
// Graph doesn't index them, and azurerm has no resource for the blob and
// file services that resource_graph would list, so diagnostic_setting
// asks for theirs with the account's.
var storageServices = []string{"blobServices/default", "fileServices/default", "queueServices/default", "tableServices/default"}

// diagnosticListParallel is how many resources diagnostic_setting asks
// for their settings at once: as many as resource_graph's child lists
// (see childListParallel), which the subscription's limiter paces.
const diagnosticListParallel = childListParallel

// DiagnosticSettingLister lists diagnostic settings as
// azurerm_monitor_diagnostic_setting: those of each resource the import's
// other services listed whose type can have them (see
// diagnosticSettingTypes), of the blob, file, queue and table services of
// each storage account among them, and, for the whole subscription
// (without --resource-group), the subscription's own. Diagnostic settings
// are extension resources (Microsoft.Insights/diagnosticSettings) that
// Resource Graph doesn't index, so each resource takes a request of its
// own: the service is opt-in, and lists after the others (see
// Provider.Profile), from what they listed, so that it asks
// only about what the import found. Named alone, it lists only the
// subscription's.
//
// Each setting records its name, its target (target_resource_id, the ID
// the target is imported by, which the generated configuration refers to)
// and the target's resource group and what manages that group, so that
// the catalogue's rules leave out the settings Azure Policy deploys and
// those of what is in a group they leave out. A resource whose settings
// can't be listed because it is gone (404) or its type or SKU has none
// (400) has none; a list refused (401, 403), throttled (429), timed out
// (408) or failed by Resource Manager (5xx), or that got no answer, fails
// the service in part, as resource_graph's child lists do (see
// azureCodes): what was listed is imported, and the failure is
// reported.
type DiagnosticSettingLister struct {
	Service
}

// diagnosticTarget is a resource whose diagnostic settings are asked for.
type diagnosticTarget struct {
	// id is the ID the target is imported by, which its settings record.
	id string
	// label starts the names of the target's settings.
	label string
	// attributes are what the target's settings record of it.
	attributes map[string]string
}

func (az *DiagnosticSettingLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return az.Run(ctx, s, az.list)
}

func (az *DiagnosticSettingLister) list() error {
	s := az.Settings()
	// Every target takes a call, for settings the import may not keep.
	if !az.Wants(diagnosticSettingType) {
		return nil
	}
	client, err := armmonitor.NewDiagnosticSettingsClient(s.credential, s.clientOptions)
	if err != nil {
		return err
	}
	targets := diagnosticTargets(s.listed)
	if s.resourceGroup == "" {
		targets = append([]diagnosticTarget{{id: "/subscriptions/" + s.subscriptionID, label: "subscription"}}, targets...)
	}
	if len(s.listed) == 0 {
		log.Printf("azurerm: %s: no other service listed resources, so only the subscription's diagnostic settings are listed; name resource_graph with it for those of the resources", diagnosticSettingService)
	}
	ctx := az.Context()
	found := make([][]harvest.Resource, len(targets))
	failed := make([]error, len(targets))
	// Every target is asked, whatever the others answer: each failure is
	// kept in failed, and the group's own error is always nil.
	var lists errgroup.Group
	lists.SetLimit(diagnosticListParallel)
	for i, target := range targets {
		lists.Go(func() error {
			found[i], failed[i] = listDiagnosticSettings(ctx, client, target, az.Provider())
			return nil
		})
	}
	_ = lists.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	var parts harvest.Parts
	for i, target := range targets {
		az.Resources = append(az.Resources, found[i]...)
		if failed[i] != nil && !parts.Add(azureCodes, "azurerm: "+diagnosticSettingService+": listing the diagnostic settings of "+target.id, failed[i]) {
			log.Printf("azurerm: %s: %s has no diagnostic settings to list: %v", diagnosticSettingService, target.id, failed[i])
		}
	}
	if az.Verbose() {
		log.Printf("azurerm: %s: asked %d resources of the %d listed for their diagnostic settings", diagnosticSettingService, len(targets), len(s.listed))
	}
	return parts.Err()
}

// listDiagnosticSettings returns target's diagnostic settings, as records
// of provider, or the error of a list that failed.
func listDiagnosticSettings(ctx context.Context, client *armmonitor.DiagnosticSettingsClient, target diagnosticTarget, provider string) ([]harvest.Resource, error) {
	// The client puts the URI after a slash of its own.
	pager := client.NewListPager(strings.TrimPrefix(target.id, "/"), nil)
	settings, err := listAll(ctx, pager, func(p armmonitor.DiagnosticSettingsClientListResponse) []*armmonitor.DiagnosticSettingsResource {
		return p.Value
	})
	if err != nil {
		return nil, err
	}
	var records []harvest.Resource
	for _, setting := range settings {
		if setting.Name == nil || *setting.Name == "" {
			continue
		}
		name := *setting.Name
		attributes := map[string]string{"name": name, "target_resource_id": target.id}
		for key, value := range target.attributes {
			attributes[key] = value
		}
		// The form azurerm writes, whatever the case of the ID Resource
		// Manager returned: the target's ID as it is imported.
		id := target.id + "/providers/Microsoft.Insights/diagnosticSettings/" + name
		records = append(records, harvest.Record(id, target.label+"_"+name, diagnosticSettingType, provider, attributes))
	}
	return records, nil
}

// diagnosticTargets returns the resources of listed whose diagnostic
// settings diagnostic_setting asks for: once each, whatever the case of
// their IDs, in the order listed, those whose type can have them (see
// hasDiagnosticSettings), each storage account followed by its services
// (see storageServices). A resource whose ID isn't a Resource Manager ID,
// such as an association azurerm imports by the IDs of the two it
// associates, isn't one.
func diagnosticTargets(listed []harvest.Resource) []diagnosticTarget {
	seen := map[string]bool{}
	var targets []diagnosticTarget
	add := func(id string, parsed armid.ResourceId, from harvest.Resource) {
		key := strings.ToUpper(id)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, diagnosticTarget{id: id, label: diagnosticLabel(parsed), attributes: targetAttributes(parsed, from)})
	}
	for _, r := range listed {
		if r.Address == nil || r.Observed == nil || r.Provider != providerName || r.Address.Type == diagnosticSettingType {
			continue
		}
		if strings.Contains(r.Observed.ID, "|") {
			continue
		}
		parsed, err := armid.ParseResourceId(r.Observed.ID)
		if err != nil {
			continue
		}
		typ := parsed.TypeString()
		if hasDiagnosticSettings(typ) {
			add(r.Observed.ID, parsed, r)
		}
		if !strings.EqualFold(typ, storageAccountType) {
			continue
		}
		for _, service := range storageServices {
			id := r.Observed.ID + "/" + service
			if parsed, err := armid.ParseResourceId(id); err == nil {
				add(id, parsed, r)
			}
		}
	}
	return targets
}

// diagnosticLabel starts the names of the settings of the resource id: its
// names, joined by underscores, without a storage service's default, and
// with the service's type, so that two resources' settings of the same
// name, such as those Azure Policy deploys, don't share a name.
func diagnosticLabel(id armid.ResourceId) string {
	names, types := id.Names(), id.Types()
	var parts []string
	for i, name := range names {
		if strings.EqualFold(name, "default") && i < len(types) {
			name = types[i]
		}
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return lastSegment(id.String())
	}
	return strings.Join(parts, "_")
}

// targetAttributes are what the settings of the resource id, listed as
// from, record of it: the resource group it is in, and what manages that
// group, as from recorded it, so that the catalogue's rules for every type
// leave out the settings of what is in a group they leave out.
func targetAttributes(id armid.ResourceId, from harvest.Resource) map[string]string {
	attributes := map[string]string{}
	if rg, ok := id.RootScope().(*armid.ResourceGroup); ok {
		attributes["resource_group_name"] = rg.Name
	}
	if by := from.Observed.Attributes["resource_group_managed_by"]; by != "" {
		attributes["resource_group_managed_by"] = by
	}
	return attributes
}
