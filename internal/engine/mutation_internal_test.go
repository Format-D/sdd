package engine

import (
	"errors"
	"testing"

	"github.com/networkteam/sdd/internal/query"
)

func TestRecordedInvocationReplay(t *testing.T) {
	cases := []struct {
		name           string
		reject         EventType
		after          EventType
		commandFailure bool
		wantAttempts   int
	}{
		{name: "command fails before completing", commandFailure: true, wantAttempts: 2},
		{name: "outcome append fails", reject: EventMutationOutcome, wantAttempts: 2},
		{name: "outcome persists but transition fails", after: EventMutationOutcome, wantAttempts: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFixtureEnv(t)
			// The durable stream includes positions the engine itself did not allocate.
			env.sink.position = 1000
			started, err := env.session.Start(env.spec, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.session.Report(started.Instance, fullDraft()); err != nil {
				t.Fatal(err)
			}
			cmd, _ := env.engine.Registry.Command("fakeWrite")
			preparations, attempts := 0, 0
			var refs []uint64
			cmd.Prepare = func(*Context) (map[string]string, error) {
				preparations++
				return map[string]string{"entryId": "20260914-010000-s-tac-new"}, nil
			}
			cmd.Fn = func(ctx *Context) error {
				if ctx.Instance != started.Instance {
					t.Fatalf("command lost instance identity: %s", ctx.Instance)
				}
				attempts++
				refs = append(refs, ctx.Intent.Ref)
				if tc.commandFailure && attempts == 1 {
					return errors.New("provider unavailable")
				}
				if err := ctx.Store.WriteEngine("findings", []query.Finding{}); err != nil {
					return err
				}
				return ctx.Store.WriteEngine("entryId", ctx.Intent.Values["entryId"])
			}
			if !tc.commandFailure {
				env.sink.failWith, env.sink.reject, env.sink.after = errors.New("event append unavailable"), tc.reject, tc.after
			}
			_, err = env.session.Answer(started.Instance, "playback", "confirm", nil, "confirmed")
			if err == nil {
				t.Fatal("the interrupted request must return its failure")
			}
			if len(refs) != 1 || refs[0] <= 1000 {
				t.Fatalf("dispatch must use the stored intent position: %v", refs)
			}
			var operationErr *OperationError
			if tc.commandFailure && !errors.As(err, &operationErr) {
				t.Fatalf("operation failure must carry its retry reference: %v", err)
			}
			env.sink.failWith = nil
			resumed, err := env.engine.ReplaySession(env.session.ID, env.session.Participant, env.sink.events,
				func(string) (*Spec, error) { return env.spec, nil }, env.sink)
			if err != nil {
				t.Fatal(err)
			}
			next, err := resumed.Retry(started.Instance, refs[0])
			if err != nil {
				t.Fatal(err)
			}
			if next.Step != "verifySummary" {
				t.Fatalf("retry should return the ordinary next position, got %s", next.Step)
			}
			if preparations != 1 || attempts != tc.wantAttempts {
				t.Fatalf("retry reuses preparation and skips completed execution: preparations=%d attempts=%d", preparations, attempts)
			}
			for _, ref := range refs {
				if ref != refs[0] {
					t.Fatal("retry changed publication identity")
				}
			}
			if pending := resumed.PendingMutation(); pending != nil {
				t.Fatalf("successful outcome left pending work: %+v", pending)
			}
			if _, err := resumed.Cancel(started.Instance, refs[0]); err == nil {
				t.Fatal("completed operation must not be cancelled")
			}
		})
	}
}

func TestCancellationReturnsWithoutReexecuting(t *testing.T) {
	for _, tc := range []struct {
		name       string
		returnStep string
		reportAtOp bool
	}{
		{name: "preceding chooser", returnStep: "playback"},
		{name: "report at automatic op keeps preceding chooser", returnStep: "playback", reportAtOp: true},
		{name: "preceding report", returnStep: "assemble"},
		{name: "no preceding interaction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newFixtureEnv(t)
			failure := env.interruptWriteAfter(t, tc.returnStep, tc.reportAtOp)
			ref, originalID := failure.Intent.Ref, failure.Intent.Values["entryId"]
			if ref == 0 || originalID == "" || failure.Err == nil {
				t.Fatalf("failure must carry its continuation reference, known identity and cause: %+v", failure)
			}

			resumed := env.replay(t)
			before := len(env.sink.events)
			cancelled, err := resumed.Cancel("i_1", ref)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := StatusRunning
			if tc.returnStep == "" {
				wantStatus = StatusAbandoned
			}
			if cancelled.Step != tc.returnStep || cancelled.Status != wantStatus || env.newCalls != 1 || resumed.PendingMutation() != nil {
				t.Fatalf("cancellation = %+v, attempts=%d pending=%+v", cancelled, env.newCalls, resumed.PendingMutation())
			}
			requireCancellationOutcome(t, env.sink.events[before:], tc.returnStep)

			resumed = env.replay(t)
			before = len(env.sink.events)
			if _, err := resumed.Cancel("i_1", ref); err != nil {
				t.Fatalf("lost cancellation response must be repeatable: %v", err)
			}
			for _, event := range env.sink.events[before:] {
				if event.Event != EventServed {
					t.Fatalf("repeated cancellation changed durable state: %+v", event)
				}
			}
			if _, err := resumed.Retry("i_1", ref); err == nil || env.newCalls != 1 {
				t.Fatalf("cancelled operation retried: %v, attempts=%d", err, env.newCalls)
			}
			if tc.returnStep == "" {
				return
			}
			inst, _ := resumed.Instance("i_1")
			if body, _ := inst.Store.Get("body"); body != fullDraft()["body"] {
				t.Fatalf("cancellation lost accepted input: %v", body)
			}

			if tc.returnStep == "playback" {
				_, err = resumed.Answer("i_1", "playback", "confirm", nil, "try a fresh operation")
			} else {
				_, err = resumed.Report("i_1", map[string]any{"body": fullDraft()["body"]})
			}
			var fresh *OperationError
			if !errors.As(err, &fresh) || fresh.Intent.Ref == ref || fresh.Intent.Values["entryId"] == originalID || env.newCalls != 2 {
				t.Fatalf("fresh interaction must create a new invocation: %v, attempts=%d", err, env.newCalls)
			}
			if _, err := resumed.Retry("i_1", ref); err == nil || env.newCalls != 2 {
				t.Fatalf("old reference dispatched after fresh intent: %v, attempts=%d", err, env.newCalls)
			}
		})
	}
}

func TestCancellationAppendFailureKeepsOperationPending(t *testing.T) {
	env := newFixtureEnv(t)
	ref := env.interruptWriteAfter(t, "playback", false).Intent.Ref
	env.sink.failWith, env.sink.reject = errors.New("cancellation append unavailable"), EventMutationOutcome
	if _, err := env.session.Cancel("i_1", ref); err == nil || env.session.CancelledMutation() != nil {
		t.Fatalf("failed cancellation reported completion: %v", err)
	}
	env.sink.failWith = nil
	resumed := env.replay(t)
	if pending := resumed.PendingMutation(); pending == nil || pending.Ref != ref {
		t.Fatalf("lost pending invocation: %+v", pending)
	}
	if _, err := resumed.Cancel("i_1", ref); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationRequiresLatestPendingReference(t *testing.T) {
	env := newFixtureEnv(t)
	intent := env.interruptWriteAfter(t, "playback", false).Intent
	for _, tc := range []struct {
		instance string
		ref      uint64
	}{
		{instance: intent.Instance, ref: intent.Ref - 1},
		{instance: intent.Instance, ref: intent.Ref + 1},
		{instance: "i_other", ref: intent.Ref},
	} {
		before := len(env.sink.events)
		if _, err := env.session.Cancel(tc.instance, tc.ref); err == nil {
			t.Fatalf("cancelled mismatched invocation: %+v", tc)
		}
		if len(env.sink.events) != before || env.session.PendingMutation().Ref != intent.Ref || env.newCalls != 1 {
			t.Fatal("invalid cancellation changed the pending invocation")
		}
	}
}

func TestFailedIntentAppendPreventsDispatch(t *testing.T) {
	env := newFixtureEnv(t)
	started, err := env.session.Start(env.spec, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.session.Report(started.Instance, fullDraft()); err != nil {
		t.Fatal(err)
	}
	cmd, _ := env.engine.Registry.Command("fakeWrite")
	cmd.Prepare = func(*Context) (map[string]string, error) { return map[string]string{"entryId": "stable"}, nil }
	env.sink.failWith, env.sink.reject = errors.New("concurrent append won"), EventMutationIntent
	_, err = env.session.Answer(started.Instance, "playback", "confirm", nil, "confirmed")
	var operationErr *OperationError
	if err == nil || errors.As(err, &operationErr) {
		t.Fatalf("append loser must receive an error without retry reference: %v", err)
	}
	if env.newCalls != 0 || env.session.PendingMutation() != nil {
		t.Fatal("unrecorded intent must neither dispatch nor become pending")
	}
}
