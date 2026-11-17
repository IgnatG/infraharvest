// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"reflect"
	"strings"
	"testing"
)

// The user confirms a drift run as its confirmation describes it, so every
// parameter of the drift tools shows there when it is set: a parameter
// added to DriftInput or DriftAcceptInput without a word in the
// confirmation fails here. Strings and lists show their value; a bool or
// a number needs its words in shown.
func TestDriftConfirmationsNameEveryParameter(t *testing.T) {
	t.Parallel()
	shown := map[string]string{
		"ForceUnlock":       "another run holds the output directory's lock",
		"Parallel":          "7 roots at once",
		"EngineParallelism": "an engine parallelism of 7",
		"WriteDecisions":    "written into the decisions file",
	}
	var in DriftAcceptInput
	var walk func(v reflect.Value, expect func(name, want string))
	walk = func(v reflect.Value, expect func(name, want string)) {
		for i := range v.NumField() {
			field, value := v.Type().Field(i), v.Field(i)
			name := field.Name
			switch value.Kind() {
			case reflect.Struct:
				walk(value, expect)
			case reflect.String:
				value.SetString("marker-" + name)
				expect(name, "marker-"+name)
			case reflect.Slice:
				value.Set(reflect.ValueOf([]string{"marker-" + name}))
				expect(name, "marker-"+name)
			case reflect.Bool:
				value.SetBool(true)
				expect(name, shown[name])
			case reflect.Int:
				value.SetInt(7)
				expect(name, shown[name])
			default:
				t.Fatalf("%s: a %s parameter: say how it shows in the confirmation and here", name, value.Kind())
			}
		}
	}
	type expectation struct{ name, want string }
	var wants []expectation
	walk(reflect.ValueOf(&in).Elem(), func(name, want string) {
		wants = append(wants, expectation{name, want})
	})
	// The rules are lines drift_accept parses: a valid one keeps the
	// message the tool would give.
	in.Rules = []string{"accept attribute=marker-Rules"}
	config := &toolConfig{path: in.Config, applies: "engine"}
	messages := map[string]string{
		"drift":        driftCheckQuestion(in.DriftInput) + "?" + driftSettings(in.DriftInput) + driftRunsTerraform(config),
		"drift_accept": driftAcceptMessage(in, in.Decisions, config, true),
	}
	for tool, message := range messages {
		for _, w := range wants {
			if tool == "drift" && isAcceptOnly(w.name) {
				continue
			}
			if w.want == "" {
				t.Errorf("%s: say how %s shows in the confirmation, and add it to shown", tool, w.name)
				continue
			}
			if !strings.Contains(message, w.want) {
				t.Errorf("%s: the confirmation doesn't show %s (%q):\n%s", tool, w.name, w.want, message)
			}
		}
	}
}

// isAcceptOnly reports whether name is a field of DriftAcceptInput's own,
// which drift doesn't take.
func isAcceptOnly(name string) bool {
	_, ok := reflect.TypeFor[DriftAcceptInput]().FieldByName(name)
	_, shared := reflect.TypeFor[DriftInput]().FieldByName(name)
	return ok && !shared
}

// A run that sets nothing beyond its roots says nothing more.
func TestDriftSettingsOfADefaultRun(t *testing.T) {
	t.Parallel()
	if got := driftSettings(DriftInput{Out: "out"}); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
}
