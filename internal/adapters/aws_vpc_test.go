// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

// vpcSource is a VPC built the way the module builds one: public subnets
// routed to the internet gateway, private ones to a NAT gateway, here in
// the second zone.
const vpcSource = `resource "aws_vpc" "main" {
  assign_generated_ipv6_cidr_block = false
  cidr_block                       = "10.0.0.0/16"
  enable_dns_support               = true
  tags                             = { Name = "shop", Team = "net" }
}
resource "aws_eip" "nat" {
  domain               = "vpc"
  instance             = ""
  ipam_pool_id         = ""
  network_border_group = "us-east-1"
  network_interface    = "eni-0a1b2c3d"
  public_ipv4_pool     = "amazon"
  tags                 = { Name = "shop-us-east-1b", Team = "net" }
}
resource "aws_internet_gateway" "main" {
  tags   = { Name = "shop", Team = "net" }
  vpc_id = aws_vpc.main.id
}
resource "aws_nat_gateway" "main" {
  allocation_id     = aws_eip.nat.id
  availability_mode = "zonal"
  connectivity_type = "public"
  private_ip        = "10.0.2.10"
  subnet_id         = aws_subnet.public_b.id
  tags              = { Name = "shop-us-east-1b", Team = "net" }
  vpc_id            = aws_vpc.main.id
}
resource "aws_route_table" "private" {
  route = [{ cidr_block = "0.0.0.0/0", gateway_id = "", nat_gateway_id = aws_nat_gateway.main.id, vpc_endpoint_id = null }]
  tags  = { Name = "shop-private", Team = "net" }
  vpc_id = aws_vpc.main.id
}
resource "aws_route_table" "public" {
  propagating_vgws = []
  route = [{ cidr_block = "0.0.0.0/0", gateway_id = aws_internet_gateway.main.id, nat_gateway_id = "" }]
  tags  = { Name = "shop-public", Team = "net" }
  vpc_id = aws_vpc.main.id
}
resource "aws_route_table_association" "private_a" {
  route_table_id = aws_route_table.private.id
  subnet_id      = aws_subnet.private_a.id
}
resource "aws_route_table_association" "private_b" {
  route_table_id = aws_route_table.private.id
  subnet_id      = aws_subnet.private_b.id
}
resource "aws_route_table_association" "public_a" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public_a.id
}
resource "aws_route_table_association" "public_b" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public_b.id
}
resource "aws_subnet" "private_a" {
  availability_zone    = "us-east-1a"
  availability_zone_id = "use1-az1"
  cidr_block           = "10.0.11.0/24"
  tags                 = { Name = "shop-private-a", Team = "net", Tier = "app" }
  vpc_id               = aws_vpc.main.id
}
resource "aws_subnet" "private_b" {
  availability_zone = "us-east-1b"
  cidr_block        = "10.0.12.0/24"
  tags              = { Name = "shop-private-b", Team = "net", Tier = "app" }
  vpc_id            = aws_vpc.main.id
}
resource "aws_subnet" "public_a" {
  availability_zone       = "us-east-1a"
  cidr_block              = "10.0.1.0/24"
  map_public_ip_on_launch = true
  tags                    = { Name = "shop-public-a", Team = "net" }
  vpc_id                  = aws_vpc.main.id
}
resource "aws_subnet" "public_b" {
  availability_zone                   = "us-east-1b"
  cidr_block                          = "10.0.2.0/24"
  enable_lni_at_device_index          = 0
  map_public_ip_on_launch             = true
  private_dns_hostname_type_on_launch = "ip-name"
  tags                                = { Name = "shop-public-b", Team = "net" }
  vpc_id                              = aws_vpc.main.id
}
`

func TestVPCMapsEveryMember(t *testing.T) {
	call, err := VPC.Map(cluster(t, vpcSource))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, VPC, call)
	for _, want := range []string{
		`name = "shop"`,
		`cidr = "10.0.0.0/16"`,
		"enable_dns_hostnames = false",
		"manage_default_network_acl = false",
		"manage_default_route_table = false",
		"manage_default_security_group = false",
		// The NAT gateway's zone first, as the module puts it in the
		// first public subnet.
		`azs = ["us-east-1b", "us-east-1a"]`,
		`public_subnets = ["10.0.2.0/24", "10.0.1.0/24"]`,
		`public_subnet_names = ["shop-public-b", "shop-public-a"]`,
		"map_public_ip_on_launch = true",
		`private_subnets = ["10.0.12.0/24", "10.0.11.0/24"]`,
		`private_subnet_names = ["shop-private-b", "shop-private-a"]`,
		"enable_nat_gateway = true",
		"single_nat_gateway = true",
		`tags = { Team = "net" }`,
		`private_subnet_tags = { Tier = "app" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "public_subnet_tags") || strings.Contains(got, "vpc_tags") {
		t.Errorf("tags every resource has set twice:\n%s", got)
	}
	// At the module's defaults.
	for _, unwanted := range []string{"enable_dns_support", "_subnet_suffix"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("call sets %s at the module's default:\n%s", unwanted, got)
		}
	}
	wantAddresses := map[string]string{
		"aws_vpc.main":                          "aws_vpc.this[0]",
		"aws_eip.nat":                           "aws_eip.nat[0]",
		"aws_internet_gateway.main":             "aws_internet_gateway.this[0]",
		"aws_nat_gateway.main":                  "aws_nat_gateway.this[0]",
		"aws_route_table.private":               "aws_route_table.private[0]",
		"aws_route_table.public":                "aws_route_table.public[0]",
		"aws_route_table_association.private_a": "aws_route_table_association.private[1]",
		"aws_route_table_association.private_b": "aws_route_table_association.private[0]",
		"aws_route_table_association.public_a":  "aws_route_table_association.public[1]",
		"aws_route_table_association.public_b":  "aws_route_table_association.public[0]",
		"aws_subnet.private_a":                  "aws_subnet.private[1]",
		"aws_subnet.private_b":                  "aws_subnet.private[0]",
		"aws_subnet.public_a":                   "aws_subnet.public[1]",
		"aws_subnet.public_b":                   "aws_subnet.public[0]",
	}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: %v", call.Addresses)
	}
	wantImports := []Import{
		{To: "aws_route.public_internet_gateway[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"},
		{To: "aws_route.private_nat_gateway[0]", Of: "aws_route_table.private", Suffix: "_0.0.0.0/0"},
	}
	if !reflect.DeepEqual(call.Imports, wantImports) {
		t.Errorf("imports: %+v", call.Imports)
	}
	if got := call.Outputs["aws_subnet.public_a"]["id"]; got != "public_subnets[1]" {
		t.Errorf("public_a's ID output: %s", got)
	}
	if got := call.Outputs["aws_vpc.main"]["id"]; got != "vpc_id" {
		t.Errorf("the VPC's ID output: %s", got)
	}
}

// A VPC on its own is the module with no subnets.
func TestVPCAlone(t *testing.T) {
	call, err := VPC.Map(cluster(t, `resource "aws_vpc" "main" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  tags                 = { Name = "shop" }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, VPC, call)
	for _, want := range []string{`name = "shop"`, "azs = []"} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	// The module's default.
	if strings.Contains(got, "enable_dns_hostnames") {
		t.Errorf("call sets enable_dns_hostnames to the module's default:\n%s", got)
	}
	if len(call.Imports) != 0 || strings.Contains(got, "public_subnets") {
		t.Errorf("subnets in the call:\n%s", got)
	}
}

// Arguments at the module's defaults are left to it: subnets named as it
// names them, suffixes, tenancy, usage metrics off, and public IPs not
// mapped. An Elastic IP whose domain is left out, at its default, is a
// VPC's.
func TestVPCLeavesModuleDefaults(t *testing.T) {
	source := strings.NewReplacer(
		`enable_dns_support               = true`, "enable_dns_support = true\n  enable_network_address_usage_metrics = false\n  instance_tenancy = \"default\"",
		`"shop-public-a"`, `"shop-public-us-east-1a"`,
		`"shop-public-b"`, `"shop-public-us-east-1b"`,
		`"shop-private-a"`, `"shop-private-us-east-1a"`,
		`"shop-private-b"`, `"shop-private-us-east-1b"`,
		"map_public_ip_on_launch = true", "map_public_ip_on_launch = false",
		`domain               = "vpc"`, "",
	).Replace(vpcSource)
	call, err := VPC.Map(cluster(t, source))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, VPC, call)
	for _, unwanted := range []string{"_subnet_names", "_subnet_suffix", "instance_tenancy", "enable_network_address_usage_metrics", "map_public_ip_on_launch", "enable_dns_support"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("call sets %s at the module's default:\n%s", unwanted, got)
		}
	}
}

func TestVPCDeclines(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, reason string }{
		"no name": {
			`tags                             = { Name = "shop", Team = "net" }`, `tags = { Team = "net" }`,
			"aws_vpc.main has no Name tag",
		},
		"another route": {
			`route = [{ cidr_block = "0.0.0.0/0", gateway_id = aws_internet_gateway.main.id, nat_gateway_id = "" }]`,
			`route = [{ cidr_block = "0.0.0.0/0", gateway_id = aws_internet_gateway.main.id }, { cidr_block = "192.168.0.0/16", vpc_peering_connection_id = "pcx-1" }]`,
			"aws_route_table.public has routes other than one default route",
		},
		"a subnet without a route table": {
			`resource "aws_route_table_association" "private_a" {
  route_table_id = aws_route_table.private.id
  subnet_id      = aws_subnet.private_a.id
}`, "",
			"aws_subnet.private_a has no route table of its own",
		},
		"a NAT gateway named otherwise": {
			`tags              = { Name = "shop-us-east-1b", Team = "net" }`, `tags = { Name = "nat", Team = "net" }`,
			`aws_nat_gateway.main isn't named "shop-us-east-1b"`,
		},
		"private subnets in other zones": {
			`availability_zone    = "us-east-1a"
  availability_zone_id = "use1-az1"`, `availability_zone = "us-east-1c"`,
			"no private subnet in us-east-1a",
		},
		"public subnets that differ": {
			`  map_public_ip_on_launch = true
  tags                    = { Name = "shop-public-a", Team = "net" }`, `  tags = { Name = "shop-public-a", Team = "net" }`,
			"differ in map_public_ip_on_launch",
		},
		"an IPv6 subnet": {
			`cidr_block        = "10.0.12.0/24"`, `cidr_block = "10.0.12.0/24"
  ipv6_cidr_block = "2600:1f18::/64"`,
			"the module can't set aws_subnet.private_b.ipv6_cidr_block",
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(vpcSource, tc.from, tc.to, 1)
			if src == vpcSource {
				t.Fatal("the test doesn't change the source")
			}
			_, err := VPC.Map(cluster(t, src))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want a decline with %q", err, tc.reason)
			}
		})
	}
}

// The VPC's main route table, which no subnet of the module's is
// associated with, stays in the root.
func TestVPCLeavesTheMainRouteTable(t *testing.T) {
	call, err := VPC.Map(cluster(t, vpcSource+`resource "aws_route_table" "main" {
  vpc_id = aws_vpc.main.id
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call.Left, []string{"aws_route_table.main"}) {
		t.Errorf("left: %v", call.Left)
	}
	if _, ok := call.Addresses["aws_route_table.main"]; ok {
		t.Error("the main route table moves into the module")
	}
}

// A regional NAT gateway is in the VPC, not a subnet.
func TestVPCDeclinesRegionalNATGateways(t *testing.T) {
	src := strings.Replace(vpcSource, "  subnet_id         = aws_subnet.public_b.id\n", "", 1)
	_, err := VPC.Map(cluster(t, src))
	if !IsDecline(err) || !strings.Contains(err.Error(), "is a regional NAT gateway") {
		t.Errorf("got %v", err)
	}
}

// vpcPerZoneSource is a VPC built the way the module builds one with
// one_nat_gateway_per_az: a NAT gateway in each zone's public subnet, and
// each private subnet routed through the one in its zone.
const vpcPerZoneSource = `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = { Name = "shop" }
}
resource "aws_eip" "a" {
  domain = "vpc"
  tags   = { Name = "shop-us-east-1a" }
}
resource "aws_eip" "b" {
  domain = "vpc"
  tags   = { Name = "shop-us-east-1b" }
}
resource "aws_internet_gateway" "main" {
  tags   = { Name = "shop" }
  vpc_id = aws_vpc.main.id
}
resource "aws_nat_gateway" "a" {
  allocation_id = aws_eip.a.id
  subnet_id     = aws_subnet.public_a.id
  tags          = { Name = "shop-us-east-1a" }
}
resource "aws_nat_gateway" "b" {
  allocation_id = aws_eip.b.id
  subnet_id     = aws_subnet.public_b.id
  tags          = { Name = "shop-us-east-1b" }
}
resource "aws_route_table" "private_a" {
  route  = [{ cidr_block = "0.0.0.0/0", nat_gateway_id = aws_nat_gateway.a.id }]
  tags   = { Name = "shop-private-us-east-1a" }
  vpc_id = aws_vpc.main.id
}
resource "aws_route_table" "private_b" {
  route  = [{ cidr_block = "0.0.0.0/0", nat_gateway_id = aws_nat_gateway.b.id }]
  tags   = { Name = "shop-private-us-east-1b" }
  vpc_id = aws_vpc.main.id
}
resource "aws_route_table" "public" {
  route  = [{ cidr_block = "0.0.0.0/0", gateway_id = aws_internet_gateway.main.id }]
  tags   = { Name = "shop-public" }
  vpc_id = aws_vpc.main.id
}
resource "aws_route_table_association" "private_a" {
  route_table_id = aws_route_table.private_a.id
  subnet_id      = aws_subnet.private_a.id
}
resource "aws_route_table_association" "private_b" {
  route_table_id = aws_route_table.private_b.id
  subnet_id      = aws_subnet.private_b.id
}
resource "aws_route_table_association" "public_a" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public_a.id
}
resource "aws_route_table_association" "public_b" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public_b.id
}
resource "aws_subnet" "private_a" {
  availability_zone = "us-east-1a"
  cidr_block        = "10.0.11.0/24"
  tags              = { Name = "shop-private-a" }
  vpc_id            = aws_vpc.main.id
}
resource "aws_subnet" "private_b" {
  availability_zone = "us-east-1b"
  cidr_block        = "10.0.12.0/24"
  tags              = { Name = "shop-private-b" }
  vpc_id            = aws_vpc.main.id
}
resource "aws_subnet" "public_a" {
  availability_zone = "us-east-1a"
  cidr_block        = "10.0.1.0/24"
  tags              = { Name = "shop-public-a" }
  vpc_id            = aws_vpc.main.id
}
resource "aws_subnet" "public_b" {
  availability_zone = "us-east-1b"
  cidr_block        = "10.0.2.0/24"
  tags              = { Name = "shop-public-b" }
  vpc_id            = aws_vpc.main.id
}
`

func TestVPCMapsANATGatewayPerZone(t *testing.T) {
	call, err := VPC.Map(cluster(t, vpcPerZoneSource))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, VPC, call)
	for _, want := range []string{
		`azs = ["us-east-1a", "us-east-1b"]`,
		`public_subnets = ["10.0.1.0/24", "10.0.2.0/24"]`,
		`private_subnets = ["10.0.11.0/24", "10.0.12.0/24"]`,
		"enable_nat_gateway = true",
		"one_nat_gateway_per_az = true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "single_nat_gateway") {
		t.Errorf("a single NAT gateway:\n%s", got)
	}
	wantAddresses := map[string]string{
		"aws_vpc.main":                          "aws_vpc.this[0]",
		"aws_eip.a":                             "aws_eip.nat[0]",
		"aws_eip.b":                             "aws_eip.nat[1]",
		"aws_internet_gateway.main":             "aws_internet_gateway.this[0]",
		"aws_nat_gateway.a":                     "aws_nat_gateway.this[0]",
		"aws_nat_gateway.b":                     "aws_nat_gateway.this[1]",
		"aws_route_table.private_a":             "aws_route_table.private[0]",
		"aws_route_table.private_b":             "aws_route_table.private[1]",
		"aws_route_table.public":                "aws_route_table.public[0]",
		"aws_route_table_association.private_a": "aws_route_table_association.private[0]",
		"aws_route_table_association.private_b": "aws_route_table_association.private[1]",
		"aws_route_table_association.public_a":  "aws_route_table_association.public[0]",
		"aws_route_table_association.public_b":  "aws_route_table_association.public[1]",
		"aws_subnet.private_a":                  "aws_subnet.private[0]",
		"aws_subnet.private_b":                  "aws_subnet.private[1]",
		"aws_subnet.public_a":                   "aws_subnet.public[0]",
		"aws_subnet.public_b":                   "aws_subnet.public[1]",
	}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: %v", call.Addresses)
	}
	wantImports := []Import{
		{To: "aws_route.public_internet_gateway[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"},
		{To: "aws_route.private_nat_gateway[0]", Of: "aws_route_table.private_a", Suffix: "_0.0.0.0/0"},
		{To: "aws_route.private_nat_gateway[1]", Of: "aws_route_table.private_b", Suffix: "_0.0.0.0/0"},
	}
	if !reflect.DeepEqual(call.Imports, wantImports) {
		t.Errorf("imports: %+v", call.Imports)
	}
	for address, want := range map[string]map[string]string{
		"aws_nat_gateway.b":         {"id": "natgw_ids[1]", "network_interface_id": "natgw_interface_ids[1]"},
		"aws_eip.b":                 {"id": "nat_ids[1]", "public_ip": "nat_public_ips[1]"},
		"aws_route_table.private_b": {"id": "private_route_table_ids[1]"},
	} {
		if !reflect.DeepEqual(call.Outputs[address], want) {
			t.Errorf("%s's outputs: %v", address, call.Outputs[address])
		}
	}
}

func TestVPCDeclinesNATGatewaysPerZone(t *testing.T) {
	for name, tc := range map[string]struct {
		replace []string // old, new pairs, replaced at once
		reason  string
	}{
		"private subnets routed across zones": {
			[]string{"aws_nat_gateway.a.id }]", "aws_nat_gateway.b.id }]", "aws_nat_gateway.b.id }]", "aws_nat_gateway.a.id }]"},
			"aws_subnet.private_a doesn't route through the NAT gateway in its zone",
		},
		"a NAT gateway no subnet routes through": {
			[]string{"route_table_id = aws_route_table.private_b.id", "route_table_id = aws_route_table.private_a.id"},
			"aws_vpc.main has a NAT gateway no private route table routes to",
		},
		"a NAT gateway named after another zone": {
			[]string{`tags          = { Name = "shop-us-east-1b" }`, `tags          = { Name = "shop-us-east-1a" }`},
			`aws_nat_gateway.b isn't named "shop-us-east-1b"`,
		},
		"a private route table not named after its zone": {
			[]string{`{ Name = "shop-private-us-east-1b" }`, `{ Name = "shop-private" }`},
			"aws_route_table.private_b isn't named shop-<suffix>-us-east-1b",
		},
		"two NAT gateways in a subnet": {
			[]string{"subnet_id     = aws_subnet.public_b.id", "subnet_id     = aws_subnet.public_a.id"},
			"aws_subnet.public_b has no NAT gateway",
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.NewReplacer(tc.replace...).Replace(vpcPerZoneSource)
			if src == vpcPerZoneSource {
				t.Fatal("the test doesn't change the source")
			}
			_, err := VPC.Map(cluster(t, src))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want a decline with %q", err, tc.reason)
			}
		})
	}
}

// With a NAT gateway in each zone, the module makes a public subnet, with
// its NAT gateway, in each zone a private subnet is in.
func TestVPCDeclinesAPublicSubnetWithoutANATGateway(t *testing.T) {
	_, err := VPC.Map(cluster(t, vpcPerZoneSource+`resource "aws_route_table_association" "public_c" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public_c.id
}
resource "aws_subnet" "public_c" {
  availability_zone = "us-east-1c"
  cidr_block        = "10.0.3.0/24"
  tags              = { Name = "shop-public-c" }
  vpc_id            = aws_vpc.main.id
}
`))
	if !IsDecline(err) || !strings.Contains(err.Error(), "aws_subnet.public_c has no NAT gateway") {
		t.Errorf("got %v", err)
	}
}
