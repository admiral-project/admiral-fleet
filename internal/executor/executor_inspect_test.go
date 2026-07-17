// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizedInspectJSONValueRedactsEnvironment(t *testing.T) {
	raw := []byte(`[{"Config":{"Env":["PUBLIC=value","PASSWORD=secret","NO_VALUE"]}}]`)

	sanitized := sanitizedInspectJSONValue(raw)
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatalf("marshal sanitized inspect: %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, "value") || strings.Contains(text, "secret") {
		t.Fatalf("inspect environment was not redacted: %s", text)
	}
	if !strings.Contains(text, `PUBLIC=[REDACTED]`) || !strings.Contains(text, `PASSWORD=[REDACTED]`) {
		t.Fatalf("inspect environment names were not preserved: %s", text)
	}
	if !strings.Contains(text, "NO_VALUE") {
		t.Fatalf("environment entry without a value was removed: %s", text)
	}
}
