package application_test

import (
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestCreateEntry_ActorPersistsWithCanonicalAndAliases(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "actor", Layer: "process", Confidence: "high",
		Body:      "Christopher is CEO of networkteam with a full-stack background.",
		Canonical: "Christopher", Aliases: []string{"Chris", "CH"},
	})
	created, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || created.EntryID == "" {
		t.Fatalf("actor CreateEntry = %+v, err %v", created, err)
	}
	e := loadEntryByID(t, f.graphDir, created.EntryID)
	if !e.IsActor() || e.Canonical != "Christopher" {
		t.Fatalf("persisted entry is not an actor with canonical Christopher: kind=%s canonical=%q", e.Kind, e.Canonical)
	}
	if len(e.Aliases) != 2 || e.Aliases[0] != "Chris" || e.Aliases[1] != "CH" {
		t.Errorf("aliases = %v, want [Chris CH]", e.Aliases)
	}
}

func TestCreateEntry_RolePassesValidationWithBoundActor(t *testing.T) {
	f := newWriteFixture(t)
	actorDraft := f.identifiedDraft(sdd.EntryDraft{Kind: "actor", Layer: "process", Confidence: "high", Canonical: "Christopher", Body: "Christopher participates in this project."})
	actor, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, actorDraft)
	if err != nil {
		t.Fatal(err)
	}
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "role", Layer: "process", Confidence: "high",
		Body: "Christopher holds the strategic and conceptual calls on this project.", Actor: "Christopher",
		Refs: []sdd.EntryRef{{ID: actor.EntryID, Kind: "grounded-in"}},
	})
	role, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || role.EntryID == "" {
		t.Fatalf("role CreateEntry with a bound actor should pass validation and write, got %+v, err %v", role, err)
	}
}

func TestCreateEntry_RoleMissingActorReturnsActionableValidationError(t *testing.T) {
	f := newWriteFixture(t)
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "role", Layer: "process", Confidence: "high",
		Body: "A role with no bound actor.",
	})
	_, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	validationErrorMentions(t, err, "actor")
}
