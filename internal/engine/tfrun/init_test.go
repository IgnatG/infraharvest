// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// failingInits is a Terraform whose inits fail with errs, in turn, and
// then succeed.
type failingInits struct {
	Terraform
	errs  []error
	inits int
}

func (f *failingInits) Init(context.Context, ...tfexec.InitOption) error {
	f.inits++
	if f.inits <= len(f.errs) {
		return f.errs[f.inits-1]
	}
	return nil
}

// An init that fails reaching a registry runs once more; one that fails
// for anything else, a second network failure, or our own timeout, don't.
// Not parallel: it sets initRetryDelay.
func TestInitRetriesANetworkFailureOnce(t *testing.T) {
	delay := initRetryDelay
	initRetryDelay = time.Millisecond
	t.Cleanup(func() { initRetryDelay = delay })

	registry := errors.New(`exit status 1

Error: Failed to query available provider packages

Could not retrieve the list of available versions for provider hashicorp/aws: could not connect to registry.terraform.io: failed to request discovery document: Get "https://registry.terraform.io/.well-known/terraform.json": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)`)
	config := errors.New(`exit status 1

Error: Unsupported argument

  on main.tf line 3, in resource "aws_s3_bucket" "logs":
   3:   buckett = "logs"`)
	for _, tc := range []struct {
		name      string
		errs      []error
		wantInits int
		wantErr   bool
	}{
		{"no failure", nil, 1, false},
		{"a registry timeout", []error{registry}, 2, false},
		{"a reset download", []error{errors.New("Error installing provider: read tcp: connection reset by peer")}, 2, false},
		{"two registry timeouts", []error{registry, registry}, 2, true},
		{"the configuration", []error{config}, 1, true},
		{"our own timeout", []error{&timeoutError{timeout: time.Minute}}, 1, true},
	} {
		tf := &failingInits{errs: tc.errs}
		err := Init(context.Background(), tf)
		if tf.inits != tc.wantInits || (err != nil) != tc.wantErr {
			t.Errorf("%s: %d inits, error %v; want %d inits, an error %v", tc.name, tf.inits, err, tc.wantInits, tc.wantErr)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tf := &failingInits{errs: []error{registry}}
	if err := Init(ctx, tf); err == nil || tf.inits > 1 {
		t.Errorf("a done context: %d inits, error %v", tf.inits, err)
	}
}
