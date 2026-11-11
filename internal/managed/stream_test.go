// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// parseWhole is Parse as it was before it decoded a state as it read it:
// the whole state unmarshalled at once. ParseFrom must agree with it.
func parseWhole(r Resources, content []byte, where string) error {
	var state struct {
		Version   int             `json:"version"`
		Resources []stateResource `json:"resources"`
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}
	if state.Version != 4 {
		return fmt.Errorf("version %d", state.Version)
	}
	for _, res := range state.Resources {
		addResource(r, res, where)
	}
	return nil
}

// streamCases are states that tell decoding as it reads from decoding
// whole apart if they differ: keys in another case or order, a key twice,
// nulls, values of the wrong type, and what follows the state.
var streamCases = []string{
	state,
	`{"version": 4, "resources": []}`,
	`{"resources": [{"mode": "managed", "type": "aws_vpc", "name": "a", "instances": [{"attributes": {"id": "vpc-1"}}]}], "version": 4}`,
	`{"Version": 4, "RESOURCES": [{"Mode": "managed", "Type": "aws_vpc", "instances": [{"attributes": {"ID": "vpc-1"}}]}]}`,
	`{"version": 4, "resources": [{"mode": "managed", "type": "aws_vpc", "instances": [{"attributes": {"id": "vpc-1"}}]}], "resources": [{"mode": "managed", "type": "aws_vpc", "instances": [{"attributes": {"id": "vpc-2"}}]}]}`,
	`{"version": 4, "resources": null}`,
	`{"version": 4, "resources": [null, {"mode": "data", "type": "aws_vpc", "instances": [{"attributes": {"id": "vpc-1"}}]}]}`,
	`{"version": 4, "outputs": {"a": {"value": [1, {"b": "c"}], "sensitive": true}}, "lineage": "x", "serial": 3, "check_results": null, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "name": "logs", "instances": [{"index_key": "a", "identity": {"bucket": "logs"}, "attributes": {"id": "logs", "arn": "arn:aws:s3:::logs", "tags": {"x": "y"}}}]}]}`,
	`{"version": 4, "resources": [{"mode": "managed", "type": "aws_subnet", "module": "module.vpc", "name": "a", "instances": [{"index_key": 0, "attributes": {"id": "subnet-1"}}, {"index_key": 1.5, "attributes": {"arn": "arn:x"}}]}]}`,
	`null`,
	`{}`,
	`[]`,
	`"state"`,
	`{"version": "4"}`,
	`{"version": 4.5}`,
	`{"version": 4, "resources": {}}`,
	`{"version": 4, "resources": [1]}`,
	`{"version": 4, "resources": [{"instances": "x"}]}`,
	`{"version": 4} {"version": 4}`,
	`{"version": 4} x`,
	`{"version": 4}   `,
	`{"version": 4`,
	`{"version": 4, "resources": [`,
	``,
}

// TestParseFromIsParseWhole checks that decoding a state as it is read
// adds what unmarshalling it whole adds, and fails where that fails.
func TestParseFromIsParseWhole(t *testing.T) {
	t.Parallel()
	for _, c := range streamCases {
		want, got := Resources{}, Resources{}
		wantErr := parseWhole(want, []byte(c), "w")
		gotErr := got.ParseFrom(strings.NewReader(c), "w")
		if (wantErr == nil) != (gotErr == nil) {
			t.Errorf("%s: whole %v, as read %v", c, wantErr, gotErr)
			continue
		}
		if gotErr != nil && len(got) != 0 {
			t.Errorf("%s: failed but added %v", c, got)
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n as read %v\n   whole %v", c, got, want)
		}
	}
}

// FuzzParseFromIsParseWhole is TestParseFromIsParseWhole on any content.
func FuzzParseFromIsParseWhole(f *testing.F) {
	for _, c := range streamCases {
		f.Add([]byte(c))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		if resourcesTwice(content) {
			// encoding/json decodes the second array into the elements of
			// the first, field by field; decodeState takes the last. No
			// state names its resources twice.
			return
		}
		want, got := Resources{}, Resources{}
		wantErr := parseWhole(want, content, "w")
		gotErr := got.ParseFrom(bytes.NewReader(content), "w")
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("whole %v, as read %v", wantErr, gotErr)
		}
		if gotErr != nil && len(got) != 0 {
			t.Fatalf("failed but added %v", got)
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("as read %v, whole %v", got, want)
		}
	})
}

// resourcesTwice reports whether content, if a JSON object, has the key
// resources more than once, in any case.
func resourcesTwice(content []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(content))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	seen := 0
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if name, _ := tok.(string); strings.EqualFold(name, "resources") {
			seen++
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
	}
	return seen > 1
}

// TestParseFromLimit checks that a state larger than stateSizeLimit
// fails as ReadState fails it, with nothing added.
func TestParseFromLimit(t *testing.T) {
	limit := stateSizeLimit
	defer func() { stateSizeLimit = limit }()
	stateSizeLimit = 1 << 10
	big := `{"version": 4, "outputs": {"a": "` + strings.Repeat("x", 2<<10) + `"}, "resources": []}`
	r := Resources{}
	if err := r.ParseFrom(strings.NewReader(big), "w"); err == nil || !strings.Contains(err.Error(), "larger than") || len(r) != 0 {
		t.Errorf("a state over the limit: %v, %v", err, r)
	}
	exact := `{"version": 4, "resources": []}`
	stateSizeLimit = int64(len(exact))
	if err := r.ParseFrom(strings.NewReader(exact), "w"); err != nil {
		t.Errorf("a state of the limit: %v", err)
	}
}

// largeState returns a state of n resources, each with attributes as many
// as a real resource's.
func largeState(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"version": 4, "terraform_version": "1.17.0", "serial": 1, "lineage": "x", "outputs": {}, "resources": [`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"mode": "managed", "type": "aws_sqs_queue", "name": "q_%d", "provider": "provider[\"registry.terraform.io/hashicorp/aws\"]", "instances": [{"schema_version": 0, "attributes": {"id": "https://sqs/q-%d", "arn": "arn:aws:sqs:eu-west-2:1:q-%d", "policy": "%s", "tags": {"team": "payments"}}, "sensitive_attributes": [], "dependencies": []}]}`, i, i, i, strings.Repeat("p", 2000))
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// BenchmarkParseFrom decodes a state of 20,000 resources, about 45 MB,
// as it reads it.
func BenchmarkParseFrom(b *testing.B) {
	content := largeState(20_000)
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	for b.Loop() {
		if err := (Resources{}).ParseFrom(bytes.NewReader(content), "w"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseWhole is the same state read whole and then unmarshalled
// whole, as a store and Parse did.
func BenchmarkParseWhole(b *testing.B) {
	content := largeState(20_000)
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	for b.Loop() {
		read, err := ReadState(bytes.NewReader(content))
		if err != nil {
			b.Fatal(err)
		}
		if err := parseWhole(Resources{}, read, "w"); err != nil {
			b.Fatal(err)
		}
	}
}
