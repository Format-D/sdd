package proctest_test

import (
	"strings"
	"testing"

	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/internal/proctest"
	sdd "github.com/networkteam/sdd/pkg/application"
)

const smokeRefID = "20260601-120000-d-tac-ref"

// The harness smoke: a world opens over real stores, a capture runs the full
// real path — assemble, guide, playback, write, summary — and the entry lands
// on disk. Procedure behavior beyond this lives in the per-procedure suites.
func TestHarnessCaptureRunsEndToEnd(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(&model.Entry{
		ID: smokeRefID, Type: model.TypeDecision, Kind: model.KindDirective, Layer: model.LayerTactical, Intent: model.IntentPending,
		Summary: "A directive the smoke capture refs.", Content: "A directive the smoke capture refs.",
	}))
	session := world.Open(t, "harness-smoke")

	serve := session.Start(t, "capture", nil)
	proctest.RequireStep(t, serve, "assemble")
	instance := serve.Instance

	session.LogRead(t, "show", []string{smokeRefID}, nil)
	serve = session.Report(t, instance, map[string]any{
		"body":        "A tactical gap: the smoke fixture observes something.",
		"entryKind":   "gap",
		"layer":       "tactical",
		"refs":        []any{map[string]any{"id": smokeRefID, "kind": "addresses"}},
		"topics":      []any{"testing/fixture"},
		"confidence":  "medium",
		"widenReport": "smoke: inspected the fixture directive in full",
	})
	proctest.RequireStep(t, serve, "playback")

	serve = session.Answer(t, instance, "playback", "confirm", nil, "capture it")
	proctest.RequireStep(t, serve, "verifySummary")
	if world.LLM.Calls("writing-guide") != 1 {
		t.Fatalf("writing guide ran %d times, want 1", world.LLM.Calls("writing-guide"))
	}

	serve = session.Answer(t, instance, "verifySummary", "faithful", map[string]any{"fidelityNote": "matches"}, "")
	proctest.RequireStatus(t, serve, "completed")
	entryID, _ := serve.Produced["entryId"].(string)
	if entryID == "" {
		t.Fatalf("capture produced no entryId: %+v", serve.Produced)
	}
	entry := proctest.LoadEntry(t, world.GraphDir, entryID)
	if !strings.Contains(entry.Content, "smoke fixture observes") {
		t.Fatalf("persisted entry body = %q", entry.Content)
	}
}

func requirePendingSessionBlocksProgression(t *testing.T, session *proctest.Session, other string) {
	t.Helper()
	world := session.World
	shell, err := session.WF.ServeShell(t.Context(), world.Identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		act  func() error
	}{
		{name: "another instance report", act: func() error { _, err := session.ReportErr(t, other, captureDraft()); return err }},
		{name: "new instance", act: func() error {
			_, err := session.WF.Start(t.Context(), world.Identity, sdd.WorkflowStartRequest{Canonical: "capture"})
			return err
		}},
		{name: "instance abandonment", act: func() error {
			_, err := session.WF.Abandon(t.Context(), world.Identity, other, "stop")
			return err
		}},
		{name: "whole session abandonment", act: func() error {
			_, err := world.App.AbandonWorkflowSession(t.Context(), world.Identity, sdd.WorkflowResumeRequest{SessionID: session.ID}, "stop")
			return err
		}},
		{name: "session conclusion", act: func() error {
			_, err := session.AnswerErr(t, shell.Instance, "junction", "conclude", nil, "done")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.act(); err == nil || !strings.Contains(err.Error(), "cancel_ref=") {
				t.Fatalf("pending operation allowed progression or lost its continuation: %v", err)
			}
		})
	}
}

func requireListedStep(t *testing.T, session *proctest.Session, instance, want string) {
	t.Helper()
	world := session.World
	listed, err := world.App.ListWorkflowSessions(t.Context(), world.Identity, "proctest")
	if err != nil {
		t.Fatal(err)
	}
	listedStep := ""
	for _, item := range listed {
		for _, open := range item.Open {
			if item.Session == session.ID && open.Instance == instance {
				listedStep = open.Step
			}
		}
	}
	if listedStep != want {
		t.Fatalf("listed step = %q, want %q", listedStep, want)
	}
}
