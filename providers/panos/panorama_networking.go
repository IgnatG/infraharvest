// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package panos

import (
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/PaloAltoNetworks/pango"
	"github.com/PaloAltoNetworks/pango/netw/interface/eth"
	"github.com/PaloAltoNetworks/pango/netw/interface/subinterface/layer2"
	"github.com/PaloAltoNetworks/pango/netw/interface/subinterface/layer3"
	"github.com/PaloAltoNetworks/pango/util"
	"github.com/PaloAltoNetworks/pango/vsys"
)

type PanoramaNetworkingGenerator struct {
	PanosService
}

func (g *PanoramaNetworkingGenerator) createResourcesFromList(
	o getGeneric,
	idPrefix string,
	useIDForResourceName bool,
	terraformResourceName string,
) (resources []terraformutils.Resource) {
	var l []string
	var err error

	switch f := o.i.(type) {
	case getListWithoutArg:
		l, err = f.GetList()
	case getListWithOneArg:
		l, err = f.GetList(o.params[0])
	case getListWithTwoArgs:
		l, err = f.GetList(o.params[0], o.params[1])
	case getListWithThreeArgs:
		l, err = f.GetList(o.params[0], o.params[1], o.params[2])
	case getListWithFourArgs:
		l, err = f.GetList(o.params[0], o.params[1], o.params[2], o.params[3])
	case getListWithFiveArgs:
		l, err = f.GetList(o.params[0], o.params[1], o.params[2], o.params[3], o.params[4])
	default:
		err = fmt.Errorf("not supported")
	}
	if err != nil || len(l) == 0 {
		return []terraformutils.Resource{}
	}

	for _, r := range l {
		id := idPrefix + r
		resources = append(resources, terraformutils.NewSimpleResource(
			id,
			normalizeResourceName(func() string {
				if useIDForResourceName {
					return id
				}

				return r
			}()),
			terraformResourceName,
			"panos"))
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createAggregateInterfaceResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.AggregateInterface.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, aggregateInterface := range l {
			if !contains(vsys.NetworkImports.Interfaces, aggregateInterface) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, aggregateInterface)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + aggregateInterface
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_aggregate_interface",
				"panos"))

			e, err := g.client.(*pango.Panorama).Network.AggregateInterface.Get(tmpl, ts, aggregateInterface)
			if err != nil {
				continue
			}

			if e.Mode == eth.ModeLayer2 || e.Mode == eth.ModeVirtualWire {
				g.Resources = append(g.Resources, g.createLayer2SubInterfaceResources(tmpl, ts, vsys.Name, layer2.EthernetInterface, aggregateInterface, e.Mode)...)
			}

			if e.Mode == eth.ModeLayer3 {
				g.Resources = append(g.Resources, g.createLayer3SubInterfaceResources(tmpl, ts, vsys.Name, layer3.EthernetInterface, aggregateInterface)...)
			}
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createBFDProfileResources(tmpl, ts string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BfdProfile, []string{tmpl, ts}},
		tmpl+":"+ts+":", false, "panos_panorama_bfd_profile",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPResource(tmpl, ts, virtualRouter string) terraformutils.Resource {
	return terraformutils.NewSimpleResource(
		tmpl+":"+ts+":"+virtualRouter,
		normalizeResourceName(tmpl+":"+ts+":"+virtualRouter),
		"panos_panorama_bgp",
		"panos")

}

func (g *PanoramaNetworkingGenerator) createBGPAggregateResources(tmpl, ts, virtualRouter string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.BgpAggregate.GetList(tmpl, ts, virtualRouter)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, bgpAggregate := range l {
		id := tmpl + ":" + ts + ":" + virtualRouter + ":" + bgpAggregate
		resources = append(resources, terraformutils.NewSimpleResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_bgp_aggregate",
			"panos"))

		resources = append(resources, g.createBGPAggregateAdvertiseFilterResources(tmpl, ts, virtualRouter, bgpAggregate)...)
		resources = append(resources, g.createBGPAggregateSuppressFilterResources(tmpl, ts, virtualRouter, bgpAggregate)...)
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createBGPAggregateAdvertiseFilterResources(tmpl, ts, virtualRouter, bgpAggregate string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpAggAdvertiseFilter, []string{tmpl, ts, virtualRouter, bgpAggregate}},
		tmpl+":"+ts+":"+virtualRouter+":"+bgpAggregate+":", true, "panos_panorama_bgp_aggregate_advertise_filter",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPAggregateSuppressFilterResources(tmpl, ts, virtualRouter, bgpAggregate string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpAggSuppressFilter, []string{tmpl, ts, virtualRouter, bgpAggregate}},
		tmpl+":"+ts+":"+virtualRouter+":"+bgpAggregate+":", true, "panos_panorama_bgp_aggregate_suppress_filter",
	)
}

// The secret argument will contain "(incorrect)", not the real value
func (g *PanoramaNetworkingGenerator) createBGPAuthProfileResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpAuthProfile, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_bgp_auth_profile",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPConditionalAdvertisementResources(tmpl, ts, virtualRouter string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.BgpConditionalAdv.GetList(tmpl, ts, virtualRouter)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, bgpConditionalAdv := range l {
		id := tmpl + ":" + ts + ":" + virtualRouter + ":" + bgpConditionalAdv
		resources = append(resources, terraformutils.NewSimpleResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_bgp_conditional_adv",
			"panos"))

		resources = append(resources, g.createBGPConditionalAdvertisementAdvertiseFilterResources(tmpl, ts, virtualRouter, bgpConditionalAdv)...)
		resources = append(resources, g.createBGPConditionalAdvertisementNonExistFilterResources(tmpl, ts, virtualRouter, bgpConditionalAdv)...)
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createBGPConditionalAdvertisementAdvertiseFilterResources(tmpl, ts, virtualRouter, bgpConditionalAdv string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpConAdvAdvertiseFilter, []string{tmpl, ts, virtualRouter, bgpConditionalAdv}},
		tmpl+":"+ts+":"+virtualRouter+":"+bgpConditionalAdv+":", true, "panos_panorama_bgp_conditional_adv_advertise_filter",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPConditionalAdvertisementNonExistFilterResources(tmpl, ts, virtualRouter, bgpConditionalAdv string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpConAdvNonExistFilter, []string{tmpl, ts, virtualRouter, bgpConditionalAdv}},
		tmpl+":"+ts+":"+virtualRouter+":"+bgpConditionalAdv+":", true, "panos_panorama_bgp_conditional_adv_non_exist_filter",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPDampeningProfileResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpDampeningProfile, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_bgp_dampening_profile",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPExportRuleGroupResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpExport, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_bgp_export_rule_group",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPImportRuleGroupResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpImport, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_bgp_import_rule_group",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPPeerGroupResources(tmpl, ts, virtualRouter string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.BgpPeerGroup.GetList(tmpl, ts, virtualRouter)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, bgpPeerGroup := range l {
		id := tmpl + ":" + ts + ":" + virtualRouter + ":" + bgpPeerGroup
		resources = append(resources, terraformutils.NewSimpleResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_bgp_peer_group",
			"panos"))

		resources = append(resources, g.createBGPPeerResources(tmpl, ts, virtualRouter, bgpPeerGroup)...)
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createBGPPeerResources(tmpl, ts, virtualRouter, bgpPeerGroup string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpPeer, []string{tmpl, ts, virtualRouter, bgpPeerGroup}},
		tmpl+":"+ts+":"+virtualRouter+":"+bgpPeerGroup+":", true, "panos_panorama_bgp_peer",
	)
}

func (g *PanoramaNetworkingGenerator) createBGPRedistResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.BgpRedistRule, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_bgp_redist_rule",
	)
}

func (g *PanoramaNetworkingGenerator) createEthernetInterfaceResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.EthernetInterface.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, ethernetInterface := range l {
			if !contains(vsys.NetworkImports.Interfaces, ethernetInterface) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, ethernetInterface)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + ethernetInterface
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_ethernet_interface",
				"panos"))

			e, err := g.client.(*pango.Panorama).Network.EthernetInterface.Get(tmpl, ts, ethernetInterface)
			if err != nil {
				continue
			}

			if e.Mode == eth.ModeLayer2 || e.Mode == eth.ModeVirtualWire {
				g.Resources = append(g.Resources, g.createLayer2SubInterfaceResources(tmpl, ts, vsys.Name, layer2.EthernetInterface, ethernetInterface, e.Mode)...)
			}

			if e.Mode == eth.ModeLayer3 {
				g.Resources = append(g.Resources, g.createLayer3SubInterfaceResources(tmpl, ts, vsys.Name, layer3.EthernetInterface, ethernetInterface)...)
			}
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createGRETunnelResources(tmpl, ts string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.GreTunnel, []string{tmpl, ts}},
		tmpl+":"+ts+":", false, "panos_panorama_gre_tunnel",
	)
}

func (g *PanoramaNetworkingGenerator) createIKECryptoProfileResources(tmpl, ts string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.IkeCryptoProfile.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	idPrefix := tmpl + ":" + ts + ":"
	for _, ikeCryptoProfile := range l {
		id := idPrefix + ikeCryptoProfile
		resources = append(resources, terraformutils.NewResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_ike_crypto_profile",
			"panos",
			map[string]string{
				"name": ikeCryptoProfile,
			}))
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createIKEGatewayResources(tmpl, ts string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.IkeGateway.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	idPrefix := tmpl + ":" + ts + ":"
	for _, ikeGateway := range l {
		id := idPrefix + ikeGateway
		resources = append(resources, terraformutils.NewResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_ike_gateway",
			"panos",
			map[string]string{
				"name": ikeGateway,
			}))
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createIPSECCryptoProfileResources(tmpl, ts string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.IpsecCryptoProfile.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	idPrefix := tmpl + ":" + ts + ":"
	for _, ipsecCryptoProfile := range l {
		id := idPrefix + ipsecCryptoProfile
		resources = append(resources, terraformutils.NewResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_ipsec_crypto_profile",
			"panos",
			map[string]string{
				"name": ipsecCryptoProfile,
			}))
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createIPSECTunnelProxyIDIPv4Resources(tmpl, ts, ipsecTunnel string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.IpsecTunnelProxyId, []string{tmpl, ts, ipsecTunnel}},
		tmpl+":"+ts+":"+ipsecTunnel+":", true, "panos_panorama_ipsec_tunnel_proxy_id_ipv4",
	)
}

func (g *PanoramaNetworkingGenerator) createIPSECTunnelResources(tmpl, ts string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.IpsecTunnel.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	idPrefix := tmpl + "::"
	for _, ipsecTunnel := range l {
		id := idPrefix + ipsecTunnel
		resources = append(resources, terraformutils.NewSimpleResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_ipsec_tunnel",
			"panos"))

		resources = append(resources, g.createIPSECTunnelProxyIDIPv4Resources(tmpl, ts, ipsecTunnel)...)
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createLayer2SubInterfaceResources(tmpl, ts, vsys, interfaceType, parentInterface, parentMode string) []terraformutils.Resource {
	// TO FIX: check disabled!
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.Layer2Subinterface, []string{tmpl, ts, interfaceType, parentInterface, parentMode}},
		tmpl+":"+ts+":"+interfaceType+":"+parentInterface+":"+parentMode+":"+vsys+":", true, "panos_panorama_layer2_subinterface",
	)
}

func (g *PanoramaNetworkingGenerator) createLayer3SubInterfaceResources(tmpl, ts, vsys, interfaceType, parentInterface string) []terraformutils.Resource {
	// TO FIX: check disabled!
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.Layer3Subinterface, []string{tmpl, ts, interfaceType, parentInterface}},
		tmpl+":"+ts+":"+interfaceType+":"+parentInterface+":"+vsys+":", true, "panos_panorama_layer3_subinterface",
	)
}

func (g *PanoramaNetworkingGenerator) createLoopbackInterfaceResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.LoopbackInterface.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, loopbackInterface := range l {
			if !contains(vsys.NetworkImports.Interfaces, loopbackInterface) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, loopbackInterface)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + loopbackInterface
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_loopback_interface",
				"panos"))
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createManagementProfileResources(tmpl, ts string) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.ManagementProfile.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	idPrefix := tmpl + ":" + ts + ":"
	for _, managementProfile := range l {
		id := idPrefix + managementProfile
		resources = append(resources, terraformutils.NewResource(
			id,
			normalizeResourceName(id),
			"panos_panorama_management_profile",
			"panos",
			map[string]string{
				"name": managementProfile,
			}))
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createMonitorProfileResources(tmpl, ts string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.MonitorProfile, []string{tmpl, ts}},
		tmpl+":"+ts+":", true, "panos_panorama_monitor_profile",
	)
}

func (g *PanoramaNetworkingGenerator) createRedistributionProfileResources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.RedistributionProfile, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_redistribution_profile_ipv4",
	)
}

func (g *PanoramaNetworkingGenerator) createStaticRouteIpv4Resources(tmpl, ts, virtualRouter string) []terraformutils.Resource {
	return g.createResourcesFromList(
		getGeneric{g.client.(*pango.Panorama).Network.StaticRoute, []string{tmpl, ts, virtualRouter}},
		tmpl+":"+ts+":"+virtualRouter+":", true, "panos_panorama_static_route_ipv4",
	)
}

func (g *PanoramaNetworkingGenerator) createTunnelInterfaceResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.TunnelInterface.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, tunnelInterface := range l {
			if !contains(vsys.NetworkImports.Interfaces, tunnelInterface) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, tunnelInterface)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + tunnelInterface
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_tunnel_interface",
				"panos"))
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createVirtualRouterResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.VirtualRouter.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, virtualRouter := range l {
			if !contains(vsys.NetworkImports.VirtualRouters, virtualRouter) {
				continue
			}

			// TODO: doesn't work!!?
			// rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, virtualRouter)
			// if err != nil || !rv {
			// 	continue
			// }

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + virtualRouter
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_virtual_router",
				"panos"))

			resources = append(resources, g.createBGPResource(tmpl, ts, virtualRouter))
			resources = append(resources, g.createBGPAggregateResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPAuthProfileResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPConditionalAdvertisementResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPDampeningProfileResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPExportRuleGroupResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPImportRuleGroupResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPPeerGroupResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createBGPRedistResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createRedistributionProfileResources(tmpl, ts, virtualRouter)...)
			resources = append(resources, g.createStaticRouteIpv4Resources(tmpl, ts, virtualRouter)...)
		}
	}

	return resources
}

// FIX: get VLANs in Vsys = None
func (g *PanoramaNetworkingGenerator) createVlanResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.Vlan.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, vlan := range l {
			if !contains(vsys.NetworkImports.Vlans, vlan) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.VlanImport, tmpl, ts, vsys.Name, vlan)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + vlan
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_vlan",
				"panos"))
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createVlanInterfaceResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	l, err := g.client.(*pango.Panorama).Network.VlanInterface.GetList(tmpl, ts)
	if err != nil {
		return []terraformutils.Resource{}
	}

	for _, vsys := range v {
		for _, vlanInterface := range l {
			if !contains(vsys.NetworkImports.Interfaces, vlanInterface) {
				continue
			}

			rv, err := g.client.(*pango.Panorama).IsImported(util.InterfaceImport, tmpl, ts, vsys.Name, vlanInterface)
			if err != nil || !rv {
				continue
			}

			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + vlanInterface
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_vlan_interface",
				"panos"))
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) createZoneResources(tmpl, ts string, v []vsys.Entry) (resources []terraformutils.Resource) {
	for _, vsys := range v {
		l, err := g.client.(*pango.Panorama).Network.Zone.GetList(tmpl, ts, vsys.Name)
		if err != nil {
			return []terraformutils.Resource{}
		}

		for _, zone := range l {
			id := tmpl + ":" + ts + ":" + vsys.Name + ":" + zone
			resources = append(resources, terraformutils.NewSimpleResource(
				id,
				normalizeResourceName(id),
				"panos_panorama_zone",
				"panos"))
		}
	}

	return resources
}

func (g *PanoramaNetworkingGenerator) InitResources() error {
	if err := g.Initialize(); err != nil {
		return err
	}

	ts, err := g.client.(*pango.Panorama).Panorama.TemplateStack.GetList()
	if err != nil {
		return err
	}

	for _, v := range ts {
		g.Resources = append(g.Resources, g.createBFDProfileResources("", v)...)
		g.Resources = append(g.Resources, g.createIKECryptoProfileResources("", v)...)
		g.Resources = append(g.Resources, g.createIKEGatewayResources("", v)...)
		g.Resources = append(g.Resources, g.createIPSECCryptoProfileResources("", v)...)
		g.Resources = append(g.Resources, g.createManagementProfileResources("", v)...)
		g.Resources = append(g.Resources, g.createMonitorProfileResources("", v)...)
	}

	tmpl, err := g.client.(*pango.Panorama).Panorama.Template.GetList()
	if err != nil {
		return err
	}

	for _, v := range tmpl {
		vsysAll, err := g.client.(*pango.Panorama).Vsys.GetAll(v, "")
		if err != nil {
			return err
		}

		g.Resources = append(g.Resources, g.createAggregateInterfaceResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createBFDProfileResources(v, "")...)
		g.Resources = append(g.Resources, g.createEthernetInterfaceResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createGRETunnelResources(v, "")...)
		g.Resources = append(g.Resources, g.createIKECryptoProfileResources(v, "")...)
		g.Resources = append(g.Resources, g.createIKEGatewayResources(v, "")...)
		g.Resources = append(g.Resources, g.createIPSECCryptoProfileResources(v, "")...)
		g.Resources = append(g.Resources, g.createIPSECTunnelResources(v, "")...)
		g.Resources = append(g.Resources, g.createLoopbackInterfaceResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createManagementProfileResources(v, "")...)
		g.Resources = append(g.Resources, g.createMonitorProfileResources(v, "")...)
		g.Resources = append(g.Resources, g.createTunnelInterfaceResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createVirtualRouterResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createVlanResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createVlanInterfaceResources(v, "", vsysAll)...)
		g.Resources = append(g.Resources, g.createZoneResources(v, "", vsysAll)...)
	}

	return nil
}
