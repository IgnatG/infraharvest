// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// generateAgain generates the queues of imports into a new directory, as
// a root an earlier import generated with previous, and returns the
// directory and the result.
func generateAgain(t *testing.T, imports []rootfile.Import, previous *Previous) (string, *Result) {
	t.Helper()
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir}
	result, err := Generate(context.Background(), tf, dir, imports, Options{TidyLabels: true, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	return dir, result
}

// addressesOf returns where result imports each resource, by import ID.
func addressesOf(result *Result) map[string]string {
	at := map[string]string{}
	for _, m := range result.Addresses {
		at[m.ID] = m.Address
	}
	return at
}

// A resource keeps its label when others are added: the only queue of a
// root, main, stays main when a second one appears, and nothing moves.
func TestGenerateKeepsLabelsWhenResourcesAreAdded(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.main"}}}
	imports := []rootfile.Import{{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"}}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.main", "b": "aws_sqs_queue.jobs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	if len(result.Moves) != 0 || len(result.Unmoved) != 0 {
		t.Errorf("moves %+v, unmoved %q, want none", result.Moves, result.Unmoved)
	}
	if exists(filepath.Join(dir, rootfile.MovesFileName)) {
		t.Errorf("%s written without moves", rootfile.MovesFileName)
	}
	if readme := readFile(t, dir, rootfile.ReadmeFileName); strings.Contains(readme, "## Moves") || strings.Contains(readme, rootfile.MovesFileName) {
		t.Errorf("README.md speaks of moves:\n%s", readme)
	}
}

// A resource renamed in the cloud gets a label from its new name, and a
// moved block from where it was; the others keep theirs.
func TestGenerateMovesWhatWasRenamed(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs"},
	}}
	imports := []rootfile.Import{{Type: "aws_sqs_queue", Name: "Billing Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"}}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.billing", "b": "aws_sqs_queue.jobs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	if want := []rootfile.Move{{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing"}}; !reflect.DeepEqual(result.Moves, want) {
		t.Errorf("moves: got %+v, want %+v", result.Moves, want)
	}
	want := `moved {
  from = aws_sqs_queue.orders
  to   = aws_sqs_queue.billing
}
`
	if got := readFile(t, dir, rootfile.MovesFileName); got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", rootfile.MovesFileName, got, want)
	}
	readme := readFile(t, dir, rootfile.ReadmeFileName)
	for _, want := range []string{"## Moves", "| `aws_sqs_queue.orders` | `aws_sqs_queue.billing` |", "or as moved for those `moves.tf` moves", "Delete `imports.tf` and `moves.tf`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
}

// Two resources whose names swapped don't take each other's addresses:
// a moved block onto an address another resource still has would be
// rejected, and a swap followed as a chain.
func TestGenerateAvoidsSwappingAddresses(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.billing"},
	}}
	imports := []rootfile.Import{{Type: "aws_sqs_queue", Name: "Billing Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Orders Queue", ID: "b"}}

	_, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.billing_2", "b": "aws_sqs_queue.orders_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	want := []rootfile.Move{
		{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing_2"},
		{From: "aws_sqs_queue.billing", To: "aws_sqs_queue.orders_2"},
	}
	if !reflect.DeepEqual(result.Moves, want) || len(result.Unmoved) != 0 {
		t.Errorf("moves: got %+v, %q, want %+v", result.Moves, result.Unmoved, want)
	}
}

// The moved blocks of a moves.tf still there are kept, and a resource
// that moves again moves on from where they took it; no resource takes
// an address they move from.
func TestGenerateChainsTheMovesOfMovesTF(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{
			{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.billing"},
			{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs"},
		},
		Moved: []rootfile.Move{{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing"}},
	}
	imports := []rootfile.Import{
		{Type: "aws_sqs_queue", Name: "Invoices Queue", ID: "a"},
		{Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"},
		{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "c"},
	}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.invoices", "b": "aws_sqs_queue.jobs", "c": "aws_sqs_queue.orders_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	want := `moved {
  from = aws_sqs_queue.orders
  to   = aws_sqs_queue.billing
}

moved {
  from = aws_sqs_queue.billing
  to   = aws_sqs_queue.invoices
}
`
	if got := readFile(t, dir, rootfile.MovesFileName); got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", rootfile.MovesFileName, got, want)
	}
}

// Moved blocks that move a resource in a cycle fail the root's moves: it
// is to be applied, and moves.tf deleted, first.
func TestGenerateFailsTheMovesOfACycle(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.jobs"}},
		Moved:     []rootfile.Move{{From: "aws_sqs_queue.x", To: "aws_sqs_queue.y"}, {From: "aws_sqs_queue.y", To: "aws_sqs_queue.x"}},
	}

	dir, result := generateAgain(t, []rootfile.Import{{Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "a"}}, previous)

	if len(result.Moves) != 0 || exists(filepath.Join(dir, rootfile.MovesFileName)) {
		t.Errorf("moves %+v written for a cycle", result.Moves)
	}
	if len(result.Unmoved) != 1 || !strings.Contains(result.Unmoved[0], "come back") || !strings.Contains(result.Unmoved[0], "delete moves.tf") {
		t.Errorf("unmoved %q, want the cycle", result.Unmoved)
	}
}

// Resources move into and out of module calls, keys and all, but not
// into or out of a module of another package, which moved blocks can't
// reach, nor onto an address another resource had, such as one deleted in
// the cloud since, which its state still has.
func TestMoves(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "state", Address: "module.old.aws_s3_bucket.this"},
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_vpc", ID: "vpc-1", Address: "aws_vpc.main"},
		{Type: "aws_route_table", ID: "rtb-1", Address: "module.vpc.aws_route_table.public[0]"},
		{Type: "aws_sqs_queue", ID: "q-1", Address: "aws_sqs_queue.a"},
		{Type: "aws_sqs_queue", ID: "q-2", Address: "aws_sqs_queue.b"},
		{Type: "aws_sqs_queue", ID: "q-3", Address: "module.jobs", Unverified: true},
	}}
	now := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this"},
		{Type: "aws_s3_bucket", ID: "state", Address: "aws_s3_bucket.state"},
		{Type: "aws_subnet", ID: "subnet-1", Address: `module.subnets.aws_subnet.this["a"]`},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.vpc.aws_vpc.this[0]"},
		{Type: "aws_route_table", ID: "rtb-1", Address: "aws_route_table.public"},
		{Type: "aws_sqs_queue", ID: "q-1", Address: "aws_sqs_queue.b"},
		{Type: "aws_sqs_queue", ID: "q-3", Address: "aws_sqs_queue.jobs"},
	}
	sources := map[string]string{"logs": "../../modules/bucket", "subnets": "./modules/subnets", "vpc": "terraform-aws-modules/vpc/aws"}

	moved, retyped, unmoved := moves(previous, now, sources)

	want := []rootfile.Move{
		{From: "module.vpc.aws_route_table.public[0]", To: "aws_route_table.public"},
		{From: "module.old.aws_s3_bucket.this", To: "aws_s3_bucket.state"},
		{From: "aws_s3_bucket.logs", To: "module.logs.aws_s3_bucket.this"},
		{From: "aws_subnet.a", To: `module.subnets.aws_subnet.this["a"]`},
		{From: "aws_vpc.main", To: "module.vpc.aws_vpc.this[0]"},
	}
	if !reflect.DeepEqual(moved, want) {
		t.Errorf("moves: got %+v, want %+v", moved, want)
	}
	wantUnmoved := []string{
		"aws_sqs_queue.a is now aws_sqs_queue.b, where another of its resources was",
	}
	if !reflect.DeepEqual(unmoved, wantUnmoved) {
		t.Errorf("unmoved: got %q, want %q", unmoved, wantUnmoved)
	}
	if len(retyped) != 0 {
		t.Errorf("retyped: %+v, want none", retyped)
	}
	if moved, retyped, unmoved := moves(nil, now, sources); moved != nil || retyped != nil || unmoved != nil {
		t.Errorf("without a previous root: %+v, %+v, %q", moved, retyped, unmoved)
	}
}

// A resource of another name of its type now is forgotten where it was,
// with a removed block: of the whole resource when all of it is of the
// other name now, else, for some of its instances or a resource of a
// module call, of a resource of the root alone that a moved block moves
// it to first. One in a call that a module from the registry makes, which
// no moved block reaches, or at an address only inferred, isn't.
func TestMovesRetypes(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_alb", ID: "lb-1", Address: "aws_alb.web"},
		{Type: "aws_alb_listener", ID: "l-1", Address: "aws_alb_listener.http[0]"},
		{Type: "aws_alb_listener", ID: "l-2", Address: "aws_alb_listener.http[1]"},
		{Type: "aws_alb_target_group", ID: "tg-1", Address: `aws_alb_target_group.app["a"]`},
		{Type: "aws_alb_target_group", ID: "tg-2", Address: `aws_alb_target_group.app["b"]`},
		{Type: "aws_alb", ID: "lb-2", Address: "module.edge.aws_alb.this"},
		{Type: "aws_alb", ID: "lb-3", Address: "module.eks.module.ingress.aws_alb.this[0]"},
		{Type: "aws_alb", ID: "lb-4", Address: "aws_alb.old", Unverified: true},
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
	}}
	now := []Managed{
		{Type: "aws_lb", ID: "lb-1", Address: "aws_lb.web"},
		{Type: "aws_lb_listener", ID: "l-1", Address: "aws_lb_listener.http[0]"},
		{Type: "aws_lb_listener", ID: "l-2", Address: "aws_lb_listener.http[1]"},
		{Type: "aws_lb_target_group", ID: "tg-1", Address: "aws_lb_target_group.a"},
		{Type: "aws_alb_target_group", ID: "tg-2", Address: `aws_alb_target_group.app["b"]`},
		{Type: "aws_lb", ID: "lb-2", Address: "module.edge.aws_lb.this"},
		{Type: "aws_lb", ID: "lb-3", Address: "aws_lb.ingress"},
		{Type: "aws_lb", ID: "lb-4", Address: "aws_lb.old"},
		{Type: "aws_s3_bucket_policy", ID: "logs", Address: "aws_s3_bucket_policy.logs"},
	}
	sources := map[string]string{"edge": "./modules/edge", "eks": "terraform-aws-modules/eks/aws"}

	moved, retyped, unmoved := moves(previous, now, sources)

	want := []rootfile.Retype{
		{Type: "aws_alb", ID: "lb-1", From: "aws_alb.web", To: "aws_lb.web", Removed: "aws_alb.web"},
		{Type: "aws_alb_listener", ID: "l-1", From: "aws_alb_listener.http[0]", To: "aws_lb_listener.http[0]", Removed: "aws_alb_listener.http"},
		{Type: "aws_alb_listener", ID: "l-2", From: "aws_alb_listener.http[1]", To: "aws_lb_listener.http[1]", Removed: "aws_alb_listener.http"},
		{Type: "aws_alb_target_group", ID: "tg-1", From: `aws_alb_target_group.app["a"]`, To: "aws_lb_target_group.a", Removed: "aws_alb_target_group.departed_app_a"},
		{Type: "aws_alb", ID: "lb-2", From: "module.edge.aws_alb.this", To: "module.edge.aws_lb.this", Removed: "aws_alb.departed_edge_this"},
	}
	if !reflect.DeepEqual(retyped, want) {
		t.Errorf("retyped: got %+v, want %+v", retyped, want)
	}
	if len(moved) != 0 {
		t.Errorf("moved: %+v, want none", moved)
	}
	for _, why := range []string{
		"module.eks.module.ingress.aws_alb.this[0] is now aws_lb.ingress, another name of its type, and a moved block can't move it out of a call module.eks (terraform-aws-modules/eks/aws) makes",
		"aws_alb.old is now aws_lb.old, another name of its type, but where the root has it is only inferred",
	} {
		if !slices.ContainsFunc(unmoved, func(u string) bool { return strings.Contains(u, why) }) {
			t.Errorf("unmoved: %q, want %q", unmoved, why)
		}
	}
	if len(unmoved) != 2 {
		t.Errorf("unmoved: %q, want two", unmoved)
	}
	// A bucket that is a bucket policy now isn't another name of its type.
	if got := Retyped(previous.Addresses, now); !slices.Contains(got, "aws_s3_bucket.logs is now aws_s3_bucket_policy.logs, of another type, not one infraharvest knows as another name of aws_s3_bucket") || len(got) != 1 {
		t.Errorf("Retyped: %q", got)
	}

	dir := t.TempDir()
	if err := rootfile.WriteMoves(dir, nil, nil, retyped); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(readFile(t, dir, rootfile.MovesFileName)), " ")
	for _, block := range []string{
		`moved { from = aws_alb_target_group.app["a"] to = aws_alb_target_group.departed_app_a }`,
		"moved { from = module.edge.aws_alb.this to = aws_alb.departed_edge_this }",
		"removed { from = aws_alb.web lifecycle { destroy = false } }",
		"removed { from = aws_alb_listener.http lifecycle { destroy = false } }",
		"removed { from = aws_alb_target_group.departed_app_a lifecycle { destroy = false } }",
		"removed { from = aws_alb.departed_edge_this lifecycle { destroy = false } }",
	} {
		if !strings.Contains(got, block) {
			t.Errorf("%s misses %q:\n%s", rootfile.MovesFileName, block, got)
		}
	}
	if strings.Count(got, "removed {") != 4 {
		t.Errorf("%s: want one removed block for each resource, got:\n%s", rootfile.MovesFileName, got)
	}
}

// What a moved block moves an instance to, for a removed block to take it
// alone, is a resource of the root of its type, named after where it
// was, that the root doesn't have.
func TestDepartedAddress(t *testing.T) {
	t.Parallel()
	taken := map[string]bool{"aws_subnet.departed_a_x": true}
	for _, tt := range []struct{ address, want string }{
		{`aws_subnet.a["x"]`, "aws_subnet.departed_a_x_2"},
		{`aws_subnet.a["x"]`, "aws_subnet.departed_a_x_3"},
		{"aws_subnet.b[0]", "aws_subnet.departed_b_0"},
		{"module.vpc.aws_subnet.private[1]", "aws_subnet.departed_vpc_private_1"},
		{`module.envs["prod-eu"].aws_s3_bucket.logs`, "aws_s3_bucket.departed_envs_prod-eu_logs"},
		{`module.a.module.b[0].aws_sqs_queue.q["a.b c"]`, "aws_sqs_queue.departed_a_b_0_q_a_b_c"},
	} {
		if got := DepartedAddress(tt.address, taken); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.address, got, tt.want)
		}
	}
}

// A root generated again, whose load balancer an earlier import gave the
// type aws_alb, another name of aws_lb, forgets it as that with a removed
// block, and imports it as aws_lb.main, the root's only one: the gate
// passes, and the README says so.
func TestGenerateForgetsAndImportsAgainWhatIsOfAnotherNameOfItsType(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{{Type: "aws_alb", ID: "arn:lb", Address: "aws_alb.web"}}}
	dir, result := generateAgain(t, []rootfile.Import{{Type: "aws_lb", Name: "web", ID: "arn:lb"}}, previous)

	if want := []rootfile.Retype{{Type: "aws_alb", ID: "arn:lb", From: "aws_alb.web", To: "aws_lb.main", Removed: "aws_alb.web"}}; !reflect.DeepEqual(result.Retyped, want) {
		t.Errorf("retyped %+v, want %+v", result.Retyped, want)
	}
	if len(result.Unmoved) != 0 || len(result.Moves) != 0 {
		t.Errorf("unmoved %q, moves %+v", result.Unmoved, result.Moves)
	}
	if got := readFile(t, dir, rootfile.MovesFileName); !strings.Contains(got, "removed {\n  from = aws_alb.web\n\n  lifecycle {\n    destroy = false\n  }\n}\n") {
		t.Errorf("%s:\n%s", rootfile.MovesFileName, got)
	}
	if got := readFile(t, dir, rootfile.ImportsFileName); !strings.Contains(got, "aws_lb.main") {
		t.Errorf("%s doesn't import aws_lb.main:\n%s", rootfile.ImportsFileName, got)
	}
	readme := readFile(t, dir, rootfile.ReadmeFileName)
	for _, want := range []string{"## Moves", "| `aws_alb.web` | `aws_lb.main` |", "or as forgotten, not destroyed, for those `moves.tf` removes", "Delete `imports.tf` and `moves.tf`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
	if !result.Gate.Named(gate.CheckMoves).Passed {
		t.Errorf("moves check: %+v", result.Gate.Named(gate.CheckMoves))
	}
}

// A root that already exists may hold a file that is a symlink, such as a
// committed moves.tf or README.md link: writing it replaces the link with
// the file, and never writes through it to its target.
func TestRootWritesReplaceSymlinks(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		name  string
		write func(dir string) error
	}{
		"moves": {rootfile.MovesFileName, func(dir string) error {
			return rootfile.WriteMoves(dir, []rootfile.Move{{From: "aws_subnet.a", To: "aws_subnet.b"}}, nil, nil)
		}},
		"readme": {rootfile.ReadmeFileName, func(dir string) error { return WriteReadme(dir, &Result{}) }},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "target")
			if err := os.WriteFile(target, []byte("outside the root\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, tt.name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := tt.write(dir); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(target); string(got) != "outside the root\n" {
				t.Errorf("wrote through the link to its target: %q", got)
			}
			info, err := os.Lstat(filepath.Join(dir, tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o644) {
				t.Errorf("%s is %v, want a regular 0644 file in place of the link", tt.name, info.Mode())
			}
		})
	}
}

func TestKeepsLabel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		old, base string
		tidy      bool
		want      bool
	}{
		{"orders", "orders", true, true},
		{"orders_2", "orders", true, true},
		{"web", "shop_prod_web", true, true},
		{"web_3", "shop_prod_web", true, true},
		{"main", "orders", true, true},
		{"main", "orders", false, false},
		{"orders", "billing", true, false},
		{"orders_x", "orders", true, false},
	} {
		if got := keepsLabel(tt.old, tt.base, tt.tidy); got != tt.want {
			t.Errorf("keepsLabel(%q, %q, %v) = %v, want %v", tt.old, tt.base, tt.tidy, got, tt.want)
		}
	}
}

// A resource keeps the label an earlier import gave it before it moved
// into a module call, and no resource takes an address another had.
func TestRelabelledKeepsLabelsAndReservedAddresses(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{
			{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this"},
			{Type: "aws_s3_bucket", ID: "old", Address: "aws_s3_bucket.state"},
		},
		Labels: map[External]string{{Type: "aws_s3_bucket", ID: "logs"}: "logs"},
	}
	imports := []rootfile.Import{
		{Type: "aws_s3_bucket", Name: "logs", ID: "logs"},
		{Type: "aws_s3_bucket", Name: "state", ID: "new"},
		{Type: "aws_s3_bucket", Name: "data", ID: "data"},
	}
	got := relabelled(imports, nil, true, previous)
	want := []rootfile.Import{
		{Type: "aws_s3_bucket", Name: "data", ID: "data"},
		{Type: "aws_s3_bucket", Name: "logs", ID: "logs"},
		{Type: "aws_s3_bucket", Name: "state_2", ID: "new"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := relabelled(imports, nil, true, nil); !reflect.DeepEqual(got, labelled(imports, nil, true)) {
		t.Errorf("without a previous root: %+v", got)
	}
}

func TestPackagedCall(t *testing.T) {
	t.Parallel()
	sources := map[string]string{"eks": "terraform-aws-modules/eks/aws", "vpc": "terraform-aws-modules/vpc/aws", "roles": "../../modules/role"}
	for address, want := range map[string]string{
		"aws_vpc.main":                                     "",
		"module.vpc.aws_vpc.this[0]":                       "",
		"module.eks.module.kms.aws_kms_key.this[0]":        "module.eks (terraform-aws-modules/eks/aws)",
		`module.eks["a.b"].module.kms.aws_kms_key.this[0]`: "module.eks (terraform-aws-modules/eks/aws)",
		`module.eks["a.b"].aws_eks_cluster.this[0]`:        "",
		"module.roles.module.policy.aws_iam_policy.this":   "",
		"module.gone.module.inner.aws_s3_bucket.this":      "",
	} {
		if got := packagedCall(address, sources); got != want {
			t.Errorf("packagedCall(%s) = %q, want %q", address, got, want)
		}
	}
}
