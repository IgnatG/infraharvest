// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// GoogleNetwork maps a VPC network, its subnetworks and its firewall rules
// onto terraform-google-modules/network/google. The module creates the
// network through the google-beta provider, keys subnetworks by region
// and name and firewall rules by name, and passes its project to all of
// them: it declines members in another project, names it can't key by
// (not literals), auto mode networks, networks with none of their
// subnetworks in the root (a Google Cloud import's global root, whose
// regions' roots have them), and firewall rules that set both source and
// destination ranges. Routes stay in the root, referring to
// the module's outputs.
var GoogleNetwork = Adapter{
	Source:   "terraform-google-modules/network/google",
	Version:  "18.3.1",
	Anchor:   "google_compute_network",
	Members:  []string{"google_compute_firewall", "google_compute_subnetwork"},
	Repeated: []string{"google_compute_firewall", "google_compute_subnetwork"},
	Inputs: []string{
		"auto_create_subnetworks", "bgp_always_compare_med", "bgp_best_path_selection_mode",
		"bgp_inter_region_cost", "description", "enable_ipv6_ula", "firewall_rules", "internal_ipv6_range",
		"mtu", "network_firewall_policy_enforcement_order", "network_name", "network_profile", "project_id",
		"routing_mode", "secondary_ranges", "subnets",
	},
	Outputs:   []string{"network_id", "network_name", "network_self_link", "subnets"},
	Providers: []string{"google-beta"},
	Map:       mapGoogleNetwork,
}

func mapGoogleNetwork(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "module.vpc.google_compute_network.network"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"id":        "network_id",
			"name":      "network_name",
			"self_link": "network_self_link",
		}},
	}
	network := Read(c.Anchor.Address(), c.Anchor.Body)
	name, ok := network.Attr("name")
	if !ok {
		return nil, Declinef("%s has no name", c.Anchor.Address())
	}
	project, ok := network.Attr("project")
	if !ok {
		return nil, Declinef("%s has no project", c.Anchor.Address())
	}
	call.Set("project_id", project)
	call.Set("network_name", name)
	auto, ok := network.Attr("auto_create_subnetworks")
	if isAuto, isLiteral := literalBool(auto); !ok || !isLiteral || isAuto {
		// Its subnetworks belong to the network, not to the module; left
		// out, the argument is true.
		return nil, Declinef("%s is an auto mode network", c.Anchor.Address())
	}
	call.Set("auto_create_subnetworks", hclwrite.TokensForValue(cty.False))
	// The module's default is GLOBAL, Google's REGIONAL: always set it.
	routing, ok := network.Attr("routing_mode")
	if !ok {
		routing = hclwrite.TokensForValue(cty.StringVal("REGIONAL"))
	}
	call.Set("routing_mode", routing)
	// What the network sets, as the module calls it.
	for _, p := range [][2]string{
		{"bgp_always_compare_med", "bgp_always_compare_med"},
		{"bgp_best_path_selection_mode", "bgp_best_path_selection_mode"},
		{"bgp_inter_region_cost", "bgp_inter_region_cost"},
		{"description", "description"},
		{"enable_ula_internal_ipv6", "enable_ipv6_ula"},
		{"internal_ipv6_range", "internal_ipv6_range"},
		{"mtu", "mtu"},
		{"network_firewall_policy_enforcement_order", "network_firewall_policy_enforcement_order"},
		{"network_profile", "network_profile"},
	} {
		arg, input := p[0], p[1]
		if value, ok := network.Attr(arg); ok {
			call.Set(input, value)
		}
	}
	// It only acts when the network is created, which an import doesn't.
	network.Ignore("delete_default_routes_on_create")
	// Terraform's own setting, which an import records at its default.
	network.IgnoreDefault("deletion_policy", cty.StringVal("DELETE"))
	if err := network.Done(); err != nil {
		return nil, err
	}

	var subnets, rules []hclwrite.Tokens
	var secondary []hclwrite.ObjectAttrTokens
	subnetKeys := map[string]bool{}
	secondaryNames := map[string]bool{}
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		if !r.RefersTo("network", c.Anchor) {
			return nil, Declinef("%s doesn't refer to %s", m.Address(), c.Anchor.Address())
		}
		if p, ok := r.Attr("project"); !ok || !sameTokens(p, project) {
			return nil, Declinef("%s isn't in the network's project, as the module puts it", m.Address())
		}
		r.IgnoreDefault("deletion_policy", cty.StringVal("DELETE"))
		// The module has no input for it; Google's default is off.
		r.IgnoreDefault("allow_subnet_cidr_routes_overlap", cty.False)
		switch m.Type {
		case "google_compute_subnetwork":
			subnet, key, ranges, err := googleSubnet(r, m)
			if err != nil {
				return nil, err
			}
			if subnetKeys[key] {
				return nil, Declinef("%s: two subnetworks are %s", c.Anchor.Address(), key)
			}
			subnetKeys[key] = true
			subnets = append(subnets, subnet)
			call.Addresses[m.Address()] = "module.subnets.google_compute_subnetwork.subnetwork[" + strconv.Quote(key) + "]"
			path := "subnets[" + strconv.Quote(key) + "]"
			call.Outputs[m.Address()] = map[string]string{}
			for _, attr := range []string{"gateway_address", "id", "ip_cidr_range", "name", "network", "project", "region", "self_link"} {
				call.Outputs[m.Address()][attr] = path + "." + attr
			}
			if ranges != nil {
				// The module keys secondary ranges by subnetwork name alone.
				_, subnetName, _ := strings.Cut(key, "/")
				if secondaryNames[subnetName] {
					return nil, Declinef("%s: two subnetworks named %s have secondary ranges", c.Anchor.Address(), subnetName)
				}
				secondaryNames[subnetName] = true
				secondary = append(secondary, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(subnetName)), Value: ranges})
			}
		case "google_compute_firewall":
			rule, ruleName, err := googleFirewallRule(r, m)
			if err != nil {
				return nil, err
			}
			rules = append(rules, rule)
			call.Addresses[m.Address()] = "module.firewall_rules.google_compute_firewall.rules[" + strconv.Quote(ruleName) + "]"
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
	}
	if len(subnets) == 0 {
		// Such as in the global root, when the subnetworks are in their
		// regions': the call would read as a network without subnetworks,
		// subnets = [], while they are managed elsewhere.
		return nil, Declinef("%s has no subnetworks in this root, which the module's subnets would take", c.Anchor.Address())
	}
	call.Set("subnets", hclwrite.TokensForTuple(subnets))
	if len(secondary) > 0 {
		call.Set("secondary_ranges", hclwrite.TokensForObject(secondary))
	}
	if len(rules) > 0 {
		call.Set("firewall_rules", hclwrite.TokensForTuple(rules))
	}
	return call, nil
}

// googleSubnet maps a subnetwork onto an element of the module's subnets,
// keyed region/name, with its secondary ranges, if it has any.
func googleSubnet(r *Reader, m Resource) (subnet hclwrite.Tokens, key string, ranges hclwrite.Tokens, err error) {
	nameTokens, ok := r.Attr("name")
	name, isLiteral := literalString(nameTokens)
	if !ok || !isLiteral {
		return nil, "", nil, Declinef("%s has no literal name to key it by", m.Address())
	}
	region, isLiteral := literalString(mustAttr(r, "region"))
	if !isLiteral {
		return nil, "", nil, Declinef("%s has no literal region to key it by", m.Address())
	}
	var o Object
	o.Set("subnet_name", nameTokens)
	o.Set("subnet_region", hclwrite.TokensForValue(cty.StringVal(region)))
	cidr, ok := r.Attr("ip_cidr_range")
	if !ok {
		return nil, "", nil, Declinef("%s has no ip_cidr_range", m.Address())
	}
	o.Set("subnet_ip", cidr)
	// The module takes these as strings: Terraform converts the bools.
	if access, ok := r.Attr("private_ip_google_access"); ok {
		o.Set("subnet_private_access", access)
	}
	for _, p := range [][2]string{
		{"description", "description"},
		{"external_ipv6_prefix", "external_ipv6_prefix"},
		{"ip_collection", "ip_collection"},
		{"ipv6_access_type", "ipv6_access_type"},
		{"private_ipv6_google_access", "subnet_private_ipv6_access"},
		{"purpose", "purpose"},
		{"role", "role"},
		{"stack_type", "stack_type"},
	} {
		arg, input := p[0], p[1]
		if value, ok := r.Attr(arg); ok {
			o.Set(input, value)
		}
	}
	logs, err := r.Block("log_config")
	if err != nil {
		return nil, "", nil, err
	}
	if logs != nil {
		o.Set("subnet_flow_logs", hclwrite.TokensForValue(cty.StringVal("true")))
		for _, p := range [][2]string{
			{"aggregation_interval", "subnet_flow_logs_interval"},
			{"filter_expr", "subnet_flow_logs_filter"},
			{"flow_sampling", "subnet_flow_logs_sampling"},
			{"metadata", "subnet_flow_logs_metadata"},
			{"metadata_fields", "subnet_flow_logs_metadata_fields"},
		} {
			arg, input := p[0], p[1]
			if value, ok := logs.Attr(arg); ok {
				o.Set(input, value)
			}
		}
	}
	var secondary []hclwrite.Tokens
	for _, b := range r.Blocks("secondary_ip_range") {
		var rng Object
		rng.Copy(b, "range_name", "ip_cidr_range", "reserved_internal_range")
		secondary = append(secondary, rng.Tokens())
	}
	if secondary != nil {
		ranges = hclwrite.TokensForTuple(secondary)
	}
	r.Ignore("network", "project")
	return o.Tokens(), region + "/" + name, ranges, nil
}

// googleFirewallRule maps a firewall rule onto an element of the module's
// firewall_rules, whose ranges are the source ranges of an ingress rule
// and the destination ranges of an egress rule.
func googleFirewallRule(r *Reader, m Resource) (hclwrite.Tokens, string, error) {
	nameTokens, ok := r.Attr("name")
	name, isLiteral := literalString(nameTokens)
	if !ok || !isLiteral {
		return nil, "", Declinef("%s has no literal name to key it by", m.Address())
	}
	var o Object
	o.Set("name", nameTokens)
	direction := "INGRESS"
	if value, ok := r.Attr("direction"); ok {
		d, isLiteral := literalString(value)
		if !isLiteral {
			return nil, "", Declinef("%s has no literal direction", m.Address())
		}
		direction = d
	}
	o.Set("direction", hclwrite.TokensForValue(cty.StringVal(direction)))
	ranges, other := "source_ranges", "destination_ranges"
	if direction == "EGRESS" {
		ranges, other = other, ranges
	}
	if value, ok := r.Attr(ranges); ok {
		o.Set("ranges", value)
	}
	if value, ok := r.Attr(other); ok && !emptyList(value) {
		return nil, "", Declinef("%s sets %s, which the module can't for an %s rule", m.Address(), other, direction)
	}
	o.Copy(r, "description", "disabled", "priority", "source_tags", "source_service_accounts", "target_tags", "target_service_accounts")
	for _, kind := range []string{"allow", "deny"} {
		var entries []hclwrite.Tokens
		for _, b := range r.Blocks(kind) {
			var e Object
			e.Copy(b, "protocol", "ports")
			entries = append(entries, e.Tokens())
		}
		if entries != nil {
			o.Set(kind, hclwrite.TokensForTuple(entries))
		}
	}
	logs, err := r.Block("log_config")
	if err != nil {
		return nil, "", err
	}
	if logs != nil {
		var l Object
		l.Copy(logs, "metadata")
		o.Set("log_config", l.Tokens())
	}
	r.Ignore("network", "project")
	return o.Tokens(), name, nil
}
