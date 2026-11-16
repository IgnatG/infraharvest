// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/report"
)

// optionsPolicy is a policy, as a user would write one, that every SQS
// queue breaks: a deny with the queue's address, and a warn whose message
// starts with it.
const optionsPolicy = `package queues

import rego.v1

deny contains {"msg": msg, "resource": address} if {
	some name, _ in input.resource.aws_sqs_queue
	address := sprintf("aws_sqs_queue.%s", [name])
	msg := sprintf("%s is not allowed by the e2e policy", [address])
}

warn contains msg if {
	some name, _ in input.resource.aws_sqs_queue
	msg := sprintf("aws_sqs_queue.%s: the e2e policy warns about every queue", [name])
}
`

// TestAWSOptionsPolicy checks --policy on import and verify, with
// conftest (the e2e-aws job installs it): every queue of
// testdata/aws-options that an import takes in (not the one tagged
// ManagedBy=Terraform, which it leaves out without --include-owned) gets
// the policy's deny, at failure, and its warn, at warning, about the queue's address, type and import ID, in report.json
// and report.md, and the import still succeeds, as policy findings never
// fail the gate. verify --policy lists the same findings.
func TestAWSOptionsPolicy(t *testing.T) {
	if _, err := exec.LookPath(gate.ConftestBinary); err != nil {
		skipUnconfigured(t, "conftest is not on PATH; --policy needs it")
	}
	e := seedOptions(t)
	policies := t.TempDir()
	if err := os.WriteFile(filepath.Join(policies, "queues.rego"), []byte(optionsPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ := e.importAWS(t, report.ExitOK, "--services=sqs", "--policy="+policies)

	queues := map[string]bool{}
	for _, r := range e.state {
		if r.Type == "aws_sqs_queue" && r.Name != "owned" {
			id, _ := r.AttributeValues["id"].(string)
			queues[id] = true
		}
	}
	if len(queues) == 0 {
		t.Fatal("testdata/aws-options created no queue")
	}
	r := optionsReport(t, out)
	denied, warned := map[string]bool{}, map[string]bool{}
	for _, f := range r.Findings {
		if f.Scanner != gate.ConftestBinary {
			continue
		}
		if f.Type != "aws_sqs_queue" || !queues[f.ID] || !strings.HasPrefix(f.Address, "aws_sqs_queue.") || f.Rule != "queues" {
			t.Errorf("a policy finding not about a queue of the estate: %+v", f)
			continue
		}
		switch f.Severity {
		case "failure":
			denied[f.ID] = true
		case "warning":
			warned[f.ID] = true
		default:
			t.Errorf("a policy finding of severity %q: %+v", f.Severity, f)
		}
	}
	for id := range queues {
		if !denied[id] || !warned[id] {
			t.Errorf("queue %s: denied %t, warned %t, want both", id, denied[id], warned[id])
		}
	}
	if md := readFile(t, filepath.Join(out, report.Dir, "report.md")); !strings.Contains(md, "is not allowed by the e2e policy") {
		t.Errorf("report.md doesn't list the policy's findings:\n%s", md)
	}

	printed, _, err := optionsRun(e.ctx, t, "verify", "--engine="+e.engineName, "--terraform-path="+e.execPath, "--policy="+policies, "--format=json", out)
	if err != nil {
		t.Fatalf("infraharvest verify --policy: %v\n%s", err, printed)
	}
	// One object per root.
	var verified []struct {
		Findings []report.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(printed), &verified); err != nil {
		t.Fatalf("verify --format json: %v\n%s", err, printed)
	}
	n := 0
	for _, root := range verified {
		for _, f := range root.Findings {
			if f.Scanner == gate.ConftestBinary && f.Type == "aws_sqs_queue" {
				n++
			}
		}
	}
	if n != 2*len(queues) {
		t.Errorf("verify --policy lists %d policy findings about queues, want %d:\n%s", n, 2*len(queues), printed)
	}
}
