package application_test

import (
	"strings"
	"testing"

	"github.com/networkteam/sdd/internal/model"
	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestCreateEntry_FocusPersistsInvolvementActorsAndWhen(t *testing.T) {
	f := newWriteFixture(t)
	targetID := f.createFocusTarget(t)

	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "focus", Layer: "tactical", Confidence: "high",
		Body:        "Advance the target directive this cycle — the current focus.",
		FocusActors: []string{"Christopher"},
		FocusWhen:   &model.FocusWhen{From: "2026-01-01", To: "2026-03-01"},
		Involvement: []model.Involvement{{
			Target: targetID,
			Actors: []string{"Christopher"}, ActorsSet: true,
			When: &model.FocusWhen{From: "2026-02-01"},
		}},
	})
	focus, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || focus.EntryID == "" {
		t.Fatalf("focus CreateEntry = %+v, err %v", focus, err)
	}

	e := loadEntryByID(t, f.graphDir, focus.EntryID)
	if !e.IsFocus() {
		t.Fatalf("persisted entry is not a focus: kind=%s", e.Kind)
	}
	if len(e.FocusActors) != 1 || e.FocusActors[0] != "Christopher" {
		t.Errorf("focus actors = %v, want [Christopher]", e.FocusActors)
	}
	if e.FocusWhen == nil || e.FocusWhen.From != "2026-01-01" || e.FocusWhen.To != "2026-03-01" {
		t.Errorf("focus when = %+v, want 2026-01-01→2026-03-01", e.FocusWhen)
	}
	if len(e.Involvement) != 1 {
		t.Fatalf("involvement = %v, want one triple", e.Involvement)
	}
	inv := e.Involvement[0]
	if inv.Target != targetID {
		t.Errorf("involvement target = %q, want %q", inv.Target, targetID)
	}
	if !inv.ActorsSet || len(inv.Actors) != 1 || inv.Actors[0] != "Christopher" {
		t.Errorf("involvement actors = %v (set %v), want [Christopher]", inv.Actors, inv.ActorsSet)
	}
	if inv.When == nil || inv.When.From != "2026-02-01" {
		t.Errorf("involvement when = %+v, want from 2026-02-01", inv.When)
	}
}

func TestCreateEntry_FocusPreservesActorsSetDistinction(t *testing.T) {
	f := newWriteFixture(t)
	targetID := f.createFocusTarget(t)

	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "focus", Layer: "tactical", Confidence: "high",
		Body: "A focus spanning two targets with different actor postures.",
		Involvement: []model.Involvement{
			// Unset: inherits the focus-level default (no actors here → nil).
			{Target: targetID},
			// Explicit empty: deliberately unattributed / pull-available.
			{Target: targetID, Actors: []string{}, ActorsSet: true},
		},
	})
	focus, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || focus.EntryID == "" {
		t.Fatalf("focus CreateEntry = %+v, err %v", focus, err)
	}

	e := loadEntryByID(t, f.graphDir, focus.EntryID)
	if len(e.Involvement) != 2 {
		t.Fatalf("involvement = %v, want two triples", e.Involvement)
	}
	if e.Involvement[0].ActorsSet {
		t.Errorf("first involvement should be unset (inherit), got ActorsSet=true actors=%v", e.Involvement[0].Actors)
	}
	if !e.Involvement[1].ActorsSet {
		t.Errorf("second involvement should be explicit-empty (pull-available), got ActorsSet=false")
	}
	if len(e.Involvement[1].Actors) != 0 {
		t.Errorf("second involvement actors = %v, want explicit empty", e.Involvement[1].Actors)
	}
}

func TestCreateEntry_FocusRendersThroughAsFocusBlock(t *testing.T) {
	f := newWriteFixture(t)
	targetID := f.createFocusTarget(t)

	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "focus", Layer: "tactical", Confidence: "high",
		Body:        "Advance the target directive — rendered through the existing focus block.",
		FocusWhen:   &model.FocusWhen{From: "2026-01-01", To: "2026-03-01"},
		Involvement: []model.Involvement{{Target: targetID}},
	})
	focus, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || focus.EntryID == "" {
		t.Fatalf("focus CreateEntry = %+v, err %v", focus, err)
	}

	view, err := f.app.View(t.Context(), f.identity, "example", sdd.ViewRequest{
		Layout: "kind(focus):active:expand(involvement):as-focus-block",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := view.Sections
	if !strings.Contains(out, focus.EntryID) {
		t.Errorf("focus block should render the focus, got %q", out)
	}
	if !strings.Contains(out, targetID) {
		t.Errorf("focus block should render the involvement target, got %q", out)
	}
	if !strings.Contains(out, "2026-01-01") {
		t.Errorf("focus block should render the focus when, got %q", out)
	}
}
