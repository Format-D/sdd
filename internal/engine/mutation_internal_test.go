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
		})
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
