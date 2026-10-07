// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import (
	"fmt"
	"slices"
	"strings"
)

func ReasoningEffortLevels() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
}

func ResolveReasoningEffort(configured, override string) (string, error) {
	effort := strings.TrimSpace(override)
	if effort == "" {
		effort = strings.TrimSpace(configured)
	}
	effort = strings.ToLower(effort)
	if effort != "" && !slices.Contains(ReasoningEffortLevels(), effort) {
		return "", fmt.Errorf("invalid reasoning effort %q for openai-codex; use one of %s", effort, strings.Join(ReasoningEffortLevels(), ", "))
	}
	return effort, nil
}
