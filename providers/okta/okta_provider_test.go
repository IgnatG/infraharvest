// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package okta

import "testing"

func TestHookServicesUseTheirListers(t *testing.T) {
	services := (&OktaProvider{}).GetSupportedService()
	if _, ok := services["okta_inline_hook"].(*InlineHookGenerator); !ok {
		t.Errorf("okta_inline_hook is listed by %T, want *InlineHookGenerator", services["okta_inline_hook"])
	}
	if _, ok := services["okta_event_hook"].(*EventHookGenerator); !ok {
		t.Errorf("okta_event_hook is listed by %T, want *EventHookGenerator", services["okta_event_hook"])
	}
}
