// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"io"
	"slices"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// StepPlans counts the plans a step of the engine ran for a root, and the
// plan files it showed: each plan of a root imports, and so reads from the
// cloud, every resource the root imports again, which makes plans what a
// root's generation costs.
type StepPlans struct {
	Step  string `json:"step"`
	Plans int    `json:"plans"`
	Shows int    `json:"shows"`
}

// Counting is a Terraform that counts the plans it runs, failed ones
// included, and the plan files it shows, by the step it is at (see Step).
// Like the engine, it expects a root's commands to run one at a time.
type Counting struct {
	Terraform
	step   string
	counts []StepPlans
}

// Step makes the commands that follow count for the step name.
func (c *Counting) Step(name string) {
	c.step = name
}

// Counts returns the counts of the steps that ran a plan or showed one,
// in the order they first did.
func (c *Counting) Counts() []StepPlans {
	return slices.Clone(c.counts)
}

// count returns the count of the current step, which it adds if it has
// none yet.
func (c *Counting) count() *StepPlans {
	if i := slices.IndexFunc(c.counts, func(s StepPlans) bool { return s.Step == c.step }); i >= 0 {
		return &c.counts[i]
	}
	c.counts = append(c.counts, StepPlans{Step: c.step})
	return &c.counts[len(c.counts)-1]
}

func (c *Counting) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	c.count().Plans++
	return c.Terraform.PlanJSON(ctx, w, opts...)
}

func (c *Counting) ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error) {
	c.count().Shows++
	return c.Terraform.ShowPlanFile(ctx, planPath, opts...)
}
