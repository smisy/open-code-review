// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import "testing"

func TestResolveReasoningEffort(t *testing.T) {
	for _, level := range ReasoningEffortLevels() {
		got, err := ResolveReasoningEffort(level, "")
		if err != nil || got != level {
			t.Errorf("level=%q got=%q err=%v", level, got, err)
		}
	}
	for _, tc := range []struct{ configured, override, want string }{
		{"", "", ""}, {" HIGH ", "", "high"}, {"high", " XHIGH ", "xhigh"},
		{"xhigh", "none", "none"}, {"turbo", "low", "low"},
	} {
		got, err := ResolveReasoningEffort(tc.configured, tc.override)
		if err != nil || got != tc.want {
			t.Errorf("config=%q override=%q got=%q err=%v", tc.configured, tc.override, got, err)
		}
	}
	for _, invalid := range []string{"off", "turbo", "ultra"} {
		if _, err := ResolveReasoningEffort("", invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}
