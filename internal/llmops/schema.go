package llmops

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// Schema names sent with a structured-output request. Providers echo them
// back on validation errors, so they name the check rather than the shape.
const (
	PreflightSchemaName    = "preflight_findings"
	WritingGuideSchemaName = "writing_guide_findings"
)

// preflightResponse is both the parse target of a pre-flight response and the
// source of the schema constraining it, so the wire shape and the parser
// cannot drift.
type preflightResponse struct {
	Findings []preflightFinding `json:"findings"`
}

type preflightFinding struct {
	Observation string `json:"observation" jsonschema:"description=one to three sentences working through what you noticed, ending in a conclusion"`
	Category    string `json:"category" jsonschema:"description=short hyphenated tag naming what the observation found"`
	Severity    string `json:"severity" jsonschema:"enum=high,enum=medium,enum=low"`
}

// writingGuideResponse plays the same double role for the writing guide.
type writingGuideResponse struct {
	Findings []writingGuideFinding `json:"findings"`
}

type writingGuideFinding struct {
	Reasoning string `json:"reasoning" jsonschema:"description=the finding's work: what you noticed, before naming any conclusion"`
	Axis      string `json:"axis" jsonschema:"enum=stranding,enum=dilution,enum=conflation,enum=pointing,enum=form"`
	Quote     string `json:"quote" jsonschema:"description=the excerpt the finding is about, reproduced exactly"`
	Repair    string `json:"repair" jsonschema:"enum=cut,enum=write-in,enum=split,enum=point,enum=reword"`
	Severity  string `json:"severity" jsonschema:"enum=substantive,enum=minor"`
}

// PreflightSchema is the response shape a pre-flight checker is constrained to
// where the provider supports structured output.
func PreflightSchema() map[string]any { return structuredSchema(&preflightResponse{}) }

// WritingGuideSchema is the writing guide's equivalent.
func WritingGuideSchema() map[string]any { return structuredSchema(&writingGuideResponse{}) }

// structuredSchema reflects v into a JSON Schema a provider accepts in strict
// mode: every object closed, every field required, nothing referenced out of
// line. The reflector already emits that shape; only the $schema declaration
// is dropped, because strict validators reject keys outside the subset they
// implement.
func structuredSchema(v any) map[string]any {
	reflector := &jsonschema.Reflector{ExpandedStruct: true, DoNotReference: true}
	raw, err := json.Marshal(reflector.Reflect(v))
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	delete(out, "$schema")
	return out
}
