// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
)

// layoutCase is a file as Terraform generates it and as the layout leaves
// it.
type layoutCase struct {
	in, want string
}

// Arguments come first and nested blocks after them, one blank line
// between the two, in nested blocks too; comments move with what they are
// above or after.
func TestLayoutSeparatesArgumentsFromBlocks(t *testing.T) {
	for name, tt := range map[string]layoutCase{
		"arguments after a block": {
			in: `resource "aws_lb" "a" {
  name = "web"
  access_logs {
    bucket = "logs"
  }
  subnets = ["a"]
}
`,
			want: `resource "aws_lb" "a" {
  name    = "web"
  subnets = ["a"]

  access_logs {
    bucket = "logs"
  }
}
`,
		},
		"nested blocks": {
			in: `resource "aws_lb_listener" "a" {
  port = 80
  default_action {
    type = "forward"
    forward {
      stickiness {
        enabled = false
      }
    }
  }
}
`,
			want: `resource "aws_lb_listener" "a" {
  port = 80

  default_action {
    type = "forward"

    forward {
      stickiness {
        enabled = false
      }
    }
  }
}
`,
		},
		"comments": {
			in: `resource "aws_lambda_function" "f" {
  # The code stays as deployed.
  filename = "lambda/f.zip"
  lifecycle {
    ignore_changes = [filename]
  }
  role = "r" # inline
}
`,
			want: `resource "aws_lambda_function" "f" {
  # The code stays as deployed.
  filename = "lambda/f.zip"
  role     = "r" # inline

  lifecycle {
    ignore_changes = [filename]
  }
}
`,
		},
		// What the layout can't place stays where it is.
		"a comment of its own": {
			in: `resource "aws_sqs_queue" "a" {
  name = "a"
  tags {
    a = "b"
  }

  # about nothing
}
`,
			want: `resource "aws_sqs_queue" "a" {
  name = "a"
  tags {
    a = "b"
  }

  # about nothing
}
`,
		},
		"data sources, not modules": {
			in: `data "aws_vpc" "main" {
  filter {
    name = "x"
  }
  id = "vpc-1"
}

module "net" {
  source = "./net"
  x {
  }
  y = 1
}
`,
			want: `data "aws_vpc" "main" {
  id = "vpc-1"

  filter {
    name = "x"
  }
}

module "net" {
  source = "./net"
  x {
  }
  y = 1
}
`,
		},
	} {
		got, ok := layout([]byte(tt.in))
		if !ok {
			t.Errorf("%s: doesn't parse", name)
			continue
		}
		if string(got) != tt.want {
			t.Errorf("%s: got\n%s\nwant\n%s", name, got, tt.want)
		}
	}
	if _, ok := layout([]byte("resource {")); ok {
		t.Error("a file that doesn't parse: want it left alone")
	}
}

// A resource's block starts with its meta-arguments, then what identifies
// it and the references to what it belongs to, then its other arguments,
// its tags last; then each nested block, lifecycle and depends_on last,
// each group apart.
func TestLayoutOrdersArguments(t *testing.T) {
	got, ok := layout([]byte(`resource "azurerm_subnet" "app" {
  address_prefixes     = ["10.0.1.0/24"]
  name                 = "app"
  resource_group_name  = azurerm_resource_group.app.name
  service_endpoints    = ["Microsoft.Storage"]
  tags                 = local.tags
  virtual_network_name = azurerm_virtual_network.app.name
  delegation {
    name = "d"
  }
  depends_on = [azurerm_resource_group.app]
  lifecycle {
    ignore_changes = [service_endpoints]
  }
  timeouts {
    create = "10m"
  }
  provider = azurerm.other
}

resource "aws_sqs_queue" "q" {
  delay_seconds = 5
  name          = "q"
  tags = {
    a = "b"
  }
  visibility_timeout_seconds = 60
}
`))
	if !ok {
		t.Fatal("doesn't parse")
	}
	s := squashed(string(got))
	for _, want := range []string{
		`resource "azurerm_subnet" "app" { provider = azurerm.other name = "app" resource_group_name = azurerm_resource_group.app.name virtual_network_name = azurerm_virtual_network.app.name address_prefixes = ["10.0.1.0/24"] service_endpoints = ["Microsoft.Storage"] tags = local.tags delegation { name = "d" } timeouts { create = "10m" } lifecycle { ignore_changes = [service_endpoints] } depends_on = [azurerm_resource_group.app] }`,
		`resource "aws_sqs_queue" "q" { name = "q" delay_seconds = 5 visibility_timeout_seconds = 60 tags = { a = "b" } }`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("got\n%s\nwant %s", got, want)
		}
	}
	for _, apart := range []string{
		"provider = azurerm.other\n\n  name ",
		"tags                 = local.tags\n\n  delegation {",
		"}\n\n  timeouts {",
		"}\n\n  lifecycle {",
		"}\n\n  depends_on = ",
	} {
		if !strings.Contains(string(got), apart) {
			t.Errorf("no blank line in %q:\n%s", apart, got)
		}
	}
}
