package llmops_test

import (
	"testing"

	"github.com/networkteam/sdd/internal/llmops"
)

// The schema a provider enforces and the struct the parser fills are one
// declaration, so this asserts the shape a strict validator requires rather
// than field-by-field agreement, which reflection already guarantees.
func TestPreflightSchemaIsStrictAndClosed(t *testing.T) {
	schema := llmops.PreflightSchema()

	if schema["additionalProperties"] != false {
		t.Errorf("root object must be closed, got %v", schema["additionalProperties"])
	}
	if _, ok := schema["$schema"]; ok {
		t.Error("$schema must be dropped: strict validators reject keys outside their subset")
	}

	findings := schema["properties"].(map[string]any)["findings"].(map[string]any)
	item := findings["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Errorf("finding object must be closed, got %v", item["additionalProperties"])
	}

	required := item["required"].([]any)
	if len(required) != 3 {
		t.Errorf("every finding field is required, got %v", required)
	}

	severity := item["properties"].(map[string]any)["severity"].(map[string]any)
	enum, ok := severity["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Fatalf("severity must be an enum of three values, got %v", severity)
	}
	for i, want := range []string{"high", "medium", "low"} {
		if enum[i] != want {
			t.Errorf("severity enum[%d] = %v, want %s", i, enum[i], want)
		}
	}
}

func TestWritingGuideSchemaCarriesItsClosedSets(t *testing.T) {
	schema := llmops.WritingGuideSchema()
	item := schema["properties"].(map[string]any)["findings"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)

	for field, want := range map[string]int{"axis": 5, "repair": 5, "severity": 2} {
		enum, ok := props[field].(map[string]any)["enum"].([]any)
		if !ok {
			t.Errorf("%s must be an enum: %v", field, props[field])
			continue
		}
		if len(enum) != want {
			t.Errorf("%s enum has %d values, want %d: %v", field, len(enum), want, enum)
		}
	}
}
