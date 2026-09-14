package application_test

import (
	"strings"
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestCreateEntry_RequiresIntentOnDirective(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "directive", Layer: "tactical", Confidence: "high",
		Body: "A directive drafted without intent.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "intent")
}

func TestCreateEntry_RejectsIntentOnNonDirective(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "gap", Layer: "tactical", Intent: "pending", Confidence: "high",
		Body: "A gap carrying a stray intent.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "intent")
}

func TestCreateEntry_RejectsClassOnNonProcedure(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "gap", Layer: "tactical", Class: "shell", Confidence: "high",
		Body: "A gap carrying a stray class.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "class")
}

func TestCreateEntry_ShellProcedureIsCapturable(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "procedure", Layer: "process", Confidence: "high",
		Canonical: "test-shell", Class: "shell",
		Body: "A shell procedure captured through the engine.",
	})
	result, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || result.EntryID == "" {
		t.Fatalf("CreateEntry = %+v, err %v", result, err)
	}
	e := loadEntryByID(t, f.graphDir, result.EntryID)
	if !e.IsShellProcedure() {
		t.Fatalf("persisted entry is not a shell procedure: kind=%s class=%s", e.Kind, e.Class)
	}
}

func TestCreateEntry_RejectsEmptyKind(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Layer: "tactical", Confidence: "high",
		Body: "A draft with no kind must not become a kindless signal.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("err = %v, want kind-required error", err)
	}
}

func TestCreateEntry_ProcedureSpecRoundTrips(t *testing.T) {
	f := newWriteFixture(t)
	spec := map[string]any{
		"params": map[string]any{
			"goalHint": map[string]any{"type": "text", "optional": true, "desc": "what the caller wants examined"},
		},
		"state": map[string]any{
			"synthesis": map[string]any{"type": "text", "desc": "the outcome the run hands back"},
		},
		"steps": []any{
			map[string]any{
				"id":      "examine",
				"collect": []any{"synthesis"},
				"transitions": []any{
					map[string]any{"when": "hasSynthesis", "to": "end(completed)"},
				},
			},
		},
	}
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "procedure", Layer: "process", Confidence: "high",
		Canonical: "test-move", ProcedureSpec: spec,
		Body: "A move captured with its workflow.\n\n## unit: examine\n\nExamine.",
	})
	result, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || result.EntryID == "" {
		t.Fatalf("CreateEntry = %+v, err %v", result, err)
	}
	e := loadEntryByID(t, f.graphDir, result.EntryID)
	if e.ProcedureSpec == nil || e.ProcedureSpec.Steps.IsZero() {
		t.Fatal("persisted entry lost its workflow frontmatter")
	}
}

func TestCreateEntry_RejectsSpecOnNonProcedure(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "gap", Layer: "tactical", Confidence: "high",
		ProcedureSpec: map[string]any{"steps": []any{map[string]any{"id": "x"}}},
		Body:          "A gap carrying a stray workflow declaration.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "procedureSpec")
}

func TestCreateEntry_RejectsUnknownSpecSection(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "procedure", Layer: "process", Confidence: "high",
		Canonical: "test-typo", ProcedureSpec: map[string]any{"stepps": []any{map[string]any{"id": "x"}}},
		Body: "A procedure whose workflow declares a typo'd section.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "procedureSpec")
}

func TestCreateEntry_RejectsSpecWithoutSteps(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "procedure", Layer: "process", Confidence: "high",
		Canonical: "test-stepless", ProcedureSpec: map[string]any{"state": map[string]any{"synthesis": map[string]any{"type": "text"}}},
		Body: "A procedure whose workflow declares no steps.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "procedureSpec")
}
