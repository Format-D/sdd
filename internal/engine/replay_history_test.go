package engine_test

import (
	"testing"

	"github.com/networkteam/sdd/internal/engine"
	"github.com/networkteam/sdd/internal/model"
)

// The revision a session ran: a middle step collecting a field, both retired
// in the revision the session is replayed against.
const historyMachineRan = `state:
    kept: {type: text, desc: survives the revision}
    retired: {type: text, desc: retired by the revision}
steps:
    - id: first
      chooser: agent
      options:
          - {choice: go, collect: [kept], to: middle}
    - id: middle
      chooser: agent
      options:
          - {choice: on, collect: [retired], to: last}
    - id: last
      chooser: agent
      options:
          - {choice: finish, to: end(completed)}`

const historyMachineNow = `state:
    kept: {type: text, desc: survives the revision}
steps:
    - id: first
      chooser: agent
      options:
          - {choice: go, collect: [kept], to: last}
    - id: last
      chooser: agent
      options:
          - {choice: finish, to: end(completed)}`

func historySpec(t *testing.T, machine string) *engine.Spec {
	t.Helper()
	content := "---\ntype: decision\nlayer: prc\nkind: procedure\ncanonical: historyproc\n" +
		machine + "\n---\n\nhistory procedure\n"
	entry, err := model.ParseEntry("20260923-120000-d-prc-hst.md", content)
	if err != nil {
		t.Fatalf("fixture entry: %v", err)
	}
	spec, err := engine.ParseSpec(entry)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func replayAgainst(t *testing.T, spec *engine.Spec, events []engine.Event) *engine.Session {
	t.Helper()
	replayed, err := engine.New(engine.NewRegistry(), engine.StaticGraphs{Graph: model.NewGraph(nil)}).
		ReplaySession("s_history", "tester", events, func(string) (*engine.Spec, error) { return spec, nil }, &recordingSink{})
	if err != nil {
		t.Fatalf("replaying a log recorded against the earlier revision: %v", err)
	}
	return replayed
}

// TestReplay_ReadsTheLogAsHistory: a session recorded against an earlier
// revision of its procedure replays against the current one — the retired
// field's start seed and report are dropped and the retired step is passed
// through (20260923-230855-d-cpt-34w).
func TestReplay_ReadsTheLogAsHistory(t *testing.T) {
	ran := historySpec(t, historyMachineRan)
	sink := &recordingSink{}
	session := engine.New(engine.NewRegistry(), engine.StaticGraphs{Graph: model.NewGraph(nil)}).NewSession("s_history", "tester", sink)
	sv, err := session.Start(ran, map[string]any{"retired": "seeded"}, "")
	if err != nil {
		t.Fatal(err)
	}
	instance := sv.Instance
	if _, err := session.Answer(instance, "first", "go", map[string]any{"kept": "k"}, ""); err != nil {
		t.Fatal(err)
	}
	atMiddle := len(sink.events)
	if _, err := session.Answer(instance, "middle", "on", map[string]any{"retired": "r"}, ""); err != nil {
		t.Fatal(err)
	}

	now := historySpec(t, historyMachineNow)

	t.Run("passes through the retired step", func(t *testing.T) {
		replayed := replayAgainst(t, now, sink.events)
		inst, _ := replayed.Instance(instance)
		if inst.Step != "last" {
			t.Fatalf("replayed step = %q, want last", inst.Step)
		}
		if kept, _ := inst.Store.Get("kept"); kept != "k" {
			t.Fatalf("kept = %v, want the logged value", kept)
		}
		if _, ok := inst.Store.Get("retired"); ok {
			t.Fatal("the retired field survived replay")
		}
		serve, err := replayed.Answer(instance, "last", "finish", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if serve.Status != engine.StatusCompleted {
			t.Fatalf("status = %s, want completed", serve.Status)
		}
	})

	t.Run("a log ending on the retired step resumes at the last known step", func(t *testing.T) {
		replayed := replayAgainst(t, now, sink.events[:atMiddle])
		inst, _ := replayed.Instance(instance)
		if inst.Step != "first" {
			t.Fatalf("replayed step = %q, want first", inst.Step)
		}
		serve, err := replayed.Answer(instance, "first", "go", map[string]any{"kept": "k"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if serve.Step != "last" {
			t.Fatalf("step after answering again = %q, want last", serve.Step)
		}
	})
}
