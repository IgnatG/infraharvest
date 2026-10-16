// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "testing"

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
