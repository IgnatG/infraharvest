// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists each domain by name, with its name as an attribute,
// from one ListDomainNames call of every engine type: an Elasticsearch
// domain, or one whose engine isn't named, as an aws_elasticsearch_domain,
// and an OpenSearch one as an aws_opensearch_domain.
func TestRecordEsListsDomainsByName(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Path != "/2015-01-01/domain" {
			return recordUnexpected(t, call)
		}
		if call.Query.Get("engineType") != "" {
			t.Errorf("domains listed by engine: %s", call.Query.Encode())
		}
		return `{"DomainNames":[
			{"DomainName":"logs","EngineType":"Elasticsearch"},
			{"DomainName":"search","EngineType":"OpenSearch"},
			{"DomainName":"legacy"}]}`
	})
	g := &EsLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_elasticsearch_domain", "logs", "logs", map[string]string{"domain_name": "logs"}},
		recordedResource{"aws_opensearch_domain", "search", "search", map[string]string{"domain_name": "search"}},
		recordedResource{"aws_elasticsearch_domain", "legacy", "legacy", map[string]string{"domain_name": "legacy"}},
	)
}

func TestRecordEsReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &EsLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
