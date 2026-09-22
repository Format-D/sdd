package engine

import "testing"

func hasCancellationLane(lanes []ServeLane) bool {
	for _, lane := range lanes {
		if lane.Name == "cancellation" {
			return true
		}
	}
	return false
}

// An instance whose operation is unfinished serves the continuation alone:
// the fixed goal and prose, none of its step's schema, unit or gate, live and
// after replay (d-tac-t6u).
func TestPendingOperationServesContinuationOnly(t *testing.T) {
	env := newFixtureEnv(t)
	failure := env.interruptWriteAfter(t, "playback", false)
	for name, session := range map[string]*Session{"live": env.session, "replayed": env.replay(t)} {
		serve, err := session.Serve("i_1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if serve.Step != failure.Intent.Step || serve.Goal != PendingOperationGoal || serve.Instructions != PendingOperationInstructions {
			t.Fatalf("%s: pending position must serve the fixed goal and prose at the operation's step: %+v", name, serve)
		}
		if serve.ReportSchema != nil || len(serve.Missing) != 0 || len(serve.Lanes) != 0 || serve.Chooser != nil || len(serve.Diagnostics) != 0 {
			t.Fatalf("%s: pending position must withhold the step's schema, unit and gate: %+v", name, serve)
		}
	}
}

// A cancellation is current on its instance until the next interaction there;
// an answer that starts no new operation ends it, live and after replay.
func TestCancellationIsCurrentUntilNextInteraction(t *testing.T) {
	env := newFixtureEnv(t)
	ref := env.interruptWriteAfter(t, "playback", false).Intent.Ref
	cancelled, err := env.session.Cancel("i_1", ref)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Cancellation == nil || cancelled.Cancellation.Intent.Ref != ref || !hasCancellationLane(cancelled.Lanes) {
		t.Fatalf("the cancelling serve must carry the cancellation and its lane: %+v", cancelled)
	}
	resumed := env.replay(t)
	if serve, err := resumed.Serve("i_1"); err != nil || serve.Cancellation == nil || !hasCancellationLane(serve.Lanes) {
		t.Fatalf("a resume before any interaction must still carry the cancellation: %+v, %v", serve, err)
	}
	adjusted, err := resumed.Answer("i_1", "playback", "adjust", nil, "adjust the draft")
	if err != nil {
		t.Fatal(err)
	}
	if adjusted.Cancellation != nil || hasCancellationLane(adjusted.Lanes) {
		t.Fatalf("an answer on the instance must end the cancellation's currency: %+v", adjusted)
	}
	if serve, err := env.replay(t).Serve("i_1"); err != nil || serve.Cancellation != nil {
		t.Fatalf("replay must derive the same: %+v, %v", serve, err)
	}
}

// A cancellation with no preceding interaction closes the instance; the serve that closes it still
// carries the cancellation and its notice, so the caller learns what happened and what was left.
func TestCancellationWithoutPrecedingInteractionServesOnClosedInstance(t *testing.T) {
	env := newFixtureEnv(t)
	ref := env.interruptWriteAfter(t, "", false).Intent.Ref
	serve, err := env.session.Cancel("i_1", ref)
	if err != nil {
		t.Fatal(err)
	}
	if serve.Status != StatusAbandoned {
		t.Fatalf("no preceding interaction: the instance must close, got %s", serve.Status)
	}
	if serve.Cancellation == nil || !serve.Cancellation.Closed || serve.Cancellation.Intent.Ref != ref || !hasCancellationLane(serve.Lanes) || serve.Instructions == "" {
		t.Fatalf("the closing serve must carry the cancellation and its notice: %+v", serve)
	}
}
