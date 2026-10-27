// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// redacted is what a secret value is replaced with in text infraharvest
// shows or writes.
const redacted = "(sensitive value)"

// redactingTerraform is a Terraform whose plans never show the secret
// values it hides: Generate's own plans have the values of the secrets it
// moved into variables (see secretValues), and providers quote a value
// they reject in their errors, such as `expected ..., got "<value>"`,
// which Terraform doesn't scrub. Every plan's diagnostics go through here,
// so the errors, logs, checks and README Generate writes from them hold
// none of the values.
type redactingTerraform struct {
	tfrun.Terraform

	mu     sync.Mutex
	values []string
	forms  []string // values, as text may quote them (see textForms)
}

// hide adds the values of secrets to those the plans' output leaves out.
func (r *redactingTerraform) hide(secrets []Secret) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, knownValues(secrets)...)
	r.forms = textForms(r.values)
}

func (r *redactingTerraform) hidden() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forms
}

// PlanJSON plans, with the hidden values replaced in every message of the
// machine-readable output and in the error.
func (r *redactingTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	forms := r.hidden()
	if len(forms) == 0 {
		return r.Terraform.PlanJSON(ctx, w, opts...)
	}
	var out bytes.Buffer
	changed, err := r.Terraform.PlanJSON(ctx, &out, opts...)
	if _, werr := w.Write(redactUIOutput(out.Bytes(), forms)); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		if msg := redactText(err.Error(), forms); msg != err.Error() {
			err = &redactedError{msg: msg, err: err}
		}
	}
	return changed, err
}

// redactedError is an error whose message had secret values replaced; it
// still unwraps to the error, so that errors.Is sees a cancellation.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }

// textForms returns values and the forms text quotes them in: escaped as a
// JSON string (with and without HTML escaping) and as a Go or HCL quoted
// string, longest first, so that a longer form is replaced before a value
// it holds.
func textForms(values []string) []string {
	seen := map[string]bool{}
	var forms []string
	add := func(s string) {
		if len(s) >= minSecretLength && !seen[s] {
			seen[s] = true
			forms = append(forms, s)
		}
	}
	for _, v := range values {
		add(v)
		if b, err := json.Marshal(v); err == nil {
			add(string(b[1 : len(b)-1]))
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(v) == nil {
			b := bytes.TrimSpace(buf.Bytes())
			add(string(b[1 : len(b)-1]))
		}
		q := strconv.Quote(v)
		add(q[1 : len(q)-1])
		q = strconv.QuoteToASCII(v)
		add(q[1 : len(q)-1])
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// redactText replaces each of forms in s with "(sensitive value)".
func redactText(s string, forms []string) string {
	for _, f := range forms {
		s = strings.ReplaceAll(s, f, redacted)
	}
	return s
}

// redactUIOutput replaces forms in Terraform's machine-readable UI output:
// in the strings of each JSON message, as decoded, so that a value the
// message escapes is found too, and in a line that isn't JSON as it is.
func redactUIOutput(out []byte, forms []string) []byte {
	var b bytes.Buffer
	for _, line := range bytes.SplitAfter(out, []byte("\n")) {
		text := bytes.TrimRight(line, "\r\n")
		end := line[len(text):]
		var msg any
		dec := json.NewDecoder(bytes.NewReader(text))
		dec.UseNumber()
		if len(text) > 0 && dec.Decode(&msg) == nil && !dec.More() {
			if clean, changed := redactJSON(msg, forms); changed {
				if encoded, err := json.Marshal(clean); err == nil {
					b.Write(encoded)
					b.Write(end)
					continue
				}
			}
			b.Write(line)
			continue
		}
		b.WriteString(redactText(string(line), forms))
	}
	return b.Bytes()
}

// redactJSON replaces forms in the strings of a decoded JSON value, keys
// included, and reports whether it replaced any.
func redactJSON(v any, forms []string) (any, bool) {
	switch v := v.(type) {
	case string:
		clean := redactText(v, forms)
		return clean, clean != v
	case map[string]any:
		out := make(map[string]any, len(v))
		changed := false
		for k, sub := range v {
			cleanKey := redactText(k, forms)
			cleanSub, subChanged := redactJSON(sub, forms)
			out[cleanKey] = cleanSub
			changed = changed || subChanged || cleanKey != k
		}
		return out, changed
	case []any:
		out := make([]any, len(v))
		changed := false
		for i, sub := range v {
			var subChanged bool
			out[i], subChanged = redactJSON(sub, forms)
			changed = changed || subChanged
		}
		return out, changed
	}
	return v, false
}

// redactResult replaces the values of secrets in the text of result that
// came from plans, which WriteReadme and the report show: the gate's
// details, why module calls were declined, the errors of what was left out
// and the warnings. Plans redact their own output already (see
// redactingTerraform); this also covers text made before a value was
// hidden.
func redactResult(result *Result, secrets []Secret) {
	forms := textForms(knownValues(secrets))
	if len(forms) == 0 {
		return
	}
	for i := range result.Gate {
		for j, d := range result.Gate[i].Details {
			result.Gate[i].Details[j] = redactText(d, forms)
		}
	}
	for i := range result.Modules {
		result.Modules[i].Declined = redactText(result.Modules[i].Declined, forms)
	}
	for i := range result.Rejected {
		for j, e := range result.Rejected[i].Errors {
			result.Rejected[i].Errors[j] = redactText(e, forms)
		}
	}
	for i, w := range result.Warnings {
		result.Warnings[i] = redactText(w, forms)
	}
}
