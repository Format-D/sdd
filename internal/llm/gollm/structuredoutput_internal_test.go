package gollm

import "testing"

// Which providers get a schema is a routing table, and the two that must not
// get one are unreachable through the exported surface without a live
// endpoint: Anthropic ignores the base-URL override, so a wire test for it
// would call the real API. Anthropic's gollm schema path replaces the system
// message with the schema, which would destroy the cacheable preamble.
func TestStructuredOutputOptionPerProvider(t *testing.T) {
	schema := map[string]any{"type": "object"}

	for _, tc := range []struct {
		provider string
		wantKey  string
	}{
		{"openai", "response_format"},
		{"mistral", ""},
		{"ollama", "format"},
		{"anthropic", ""},
		{"made-up", ""},
	} {
		key, value := structuredOutputOption(tc.provider, "preflight_findings", schema)
		if key != tc.wantKey {
			t.Errorf("%s: key = %q, want %q", tc.provider, key, tc.wantKey)
		}
		if tc.wantKey == "" && value != nil {
			t.Errorf("%s: value must be nil when no key applies, got %v", tc.provider, value)
		}
	}

	_, value := structuredOutputOption("ollama", "preflight_findings", schema)
	if got, ok := value.(map[string]any); !ok || got["type"] != "object" {
		t.Errorf("ollama carries the schema bare, got %v", value)
	}
}
