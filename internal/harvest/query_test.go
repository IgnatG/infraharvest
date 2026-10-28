// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// A resource Terraform's list resources return is told by its identity's
// ID, has the identity as its attributes, and keeps a copy of its own.
func TestRecordIdentity(t *testing.T) {
	t.Parallel()
	identity := map[string]string{"name": "/app", "region": "eu-west-2"}
	r := RecordIdentity(identity, "/app", "aws_cloudwatch_log_group", "aws")
	identity["name"] = "changed"
	if r.Observed.ID != "name=/app,region=eu-west-2" || r.RawName != "/app" || r.Address.Type != "aws_cloudwatch_log_group" || r.Provider != "aws" {
		t.Errorf("got %+v %+v", r, *r.Observed)
	}
	want := map[string]string{"name": "/app", "region": "eu-west-2"}
	if !reflect.DeepEqual(r.Observed.Identity, want) || !reflect.DeepEqual(r.Observed.Attributes, want) {
		t.Errorf("identity %v, attributes %v", r.Observed.Identity, r.Observed.Attributes)
	}
}

// The query service lists with the Query its context carries, keeps what
// a partial Query listed, applies --where but not --tag, and fails without
// a Query.
func TestQueryLister(t *testing.T) {
	t.Parallel()
	listed := []Resource{
		RecordIdentity(map[string]string{"name": "a", "region": "eu-west-2"}, "a", "aws_cloudwatch_log_group", "aws"),
		RecordIdentity(map[string]string{"name": "b", "region": "eu-west-1"}, "b", "aws_cloudwatch_log_group", "aws"),
	}
	partial := Partial(errors.New("aws_ebs_volume: limit reached"))
	for name, tt := range map[string]struct {
		query   Query
		filters []Filter
		want    []string
		err     error
	}{
		"all": {
			query: func(context.Context) ([]Resource, error) { return listed, nil },
			want:  []string{"name=a,region=eu-west-2", "name=b,region=eu-west-1"},
		},
		"where and tag": {
			query:   func(context.Context) ([]Resource, error) { return listed, nil },
			filters: []Filter{{Path: "region", Values: []string{"eu-west-2"}}, {Path: "tags.env", Values: []string{"prod"}}},
			want:    []string{"name=a,region=eu-west-2"},
		},
		"partial": {
			query: func(context.Context) ([]Resource, error) { return listed[:1], partial },
			want:  []string{"name=a,region=eu-west-2"},
			err:   partial,
		},
		"failed": {
			query: func(context.Context) ([]Resource, error) { return listed, errors.New("no terraform") },
		},
		"no query": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := &QueryLister{}
			l.SetFilters(tt.filters)
			ctx := context.Background()
			if tt.query != nil {
				ctx = WithQuery(ctx, tt.query)
			}
			l.SetContext(ctx)
			err := l.List()
			var got []string
			for _, r := range l.Listed() {
				got = append(got, r.Observed.ID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listed %q, want %q", got, tt.want)
			}
			switch {
			case tt.err != nil && !errors.Is(err, tt.err):
				t.Errorf("err %v, want %v", err, tt.err)
			case tt.err == nil && tt.want != nil && err != nil:
				t.Errorf("err %v", err)
			case tt.want == nil && err == nil:
				t.Error("want an error")
			}
		})
	}
}

// An --id of an identity keeps the resource whose ID it is, though its
// commas would otherwise separate IDs.
func TestIDFilterOfAnIdentity(t *testing.T) {
	t.Parallel()
	a := RecordIdentity(map[string]string{"name": "a", "region": "eu-west-2"}, "a", "aws_cloudwatch_log_group", "aws")
	b := RecordIdentity(map[string]string{"name": "b", "region": "eu-west-2"}, "b", "aws_cloudwatch_log_group", "aws")
	f, err := ParseIDFilter("cloudwatch_log_group=" + a.Observed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Keeps(a) || f.Keeps(b) {
		t.Errorf("filter %+v: keeps a %v, b %v; want a alone", f, f.Keeps(a), f.Keeps(b))
	}
}
