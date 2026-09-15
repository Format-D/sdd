package mcpapp_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdd "github.com/networkteam/sdd/pkg/application"

	"github.com/networkteam/sdd/internal/engine"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

func TestNextRejectsEmptyReportBeforeLabelWrite(t *testing.T) {
	var sessions sdd.SessionStore
	env := newTestServer(t, nil, "", "", func(opts *testServerOptions) {
		sessions = opts.Application.Sessions
	})
	client := connect(t, env.srv)
	door := openSession(t, client)
	before, err := sessions.Load(t.Context(), sdd.SessionID(door.Session))
	if err != nil {
		t.Fatal(err)
	}
	message := callExpectError(t, client, "next", map[string]any{
		"session": door.Session, "instance": door.Instance, "report": map[string]any{}, "label": "must not be recorded",
	})
	if !strings.Contains(message, "report must not be empty") {
		t.Fatalf("empty report was not rejected: %s", message)
	}
	after, err := sessions.Load(t.Context(), sdd.SessionID(door.Session))
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || after.Metadata.Label != before.Metadata.Label {
		t.Fatalf("rejected report changed version %d to %d or label %q to %q", before.Version, after.Version, before.Metadata.Label, after.Metadata.Label)
	}
}

func TestResumeAdvertisesPendingOperationAfterLostOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect func(*testing.T, *mcp.ClientSession, string, *sdd.WorkflowPendingOperation)
	}{
		{name: "retry", expect: expectRetryContinuation},
		{name: "cancel", expect: expectCancelContinuation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &sessionStoreProbe{}
			env := newTestServer(t, nil, "", "", func(opts *testServerOptions) {
				store.SessionStore = opts.Application.Sessions
				opts.Application.Sessions = store
			})
			client := connect(t, env.srv)
			session := openSession(t, client).Session
			var serve mcpserver.ServeResult
			call(t, client, "start_procedure", map[string]any{"session": session, "canonical": "capture"}, &serve)
			instance := serve.Instance
			call(t, client, "next", map[string]any{"session": session, "instance": instance, "report": assembleReport()}, &serve)

			store.FailNextEvent(engine.EventMutationOutcome)
			message := callExpectError(t, client, "next", map[string]any{"session": session, "instance": instance, "report": map[string]any{
				"chooser": "playback", "choice": "confirm", "userWords": "capture it",
			}})
			if !strings.Contains(message, "session event append unavailable") || strings.Contains(message, "retry_ref=") || strings.Contains(message, "cancel_ref=") {
				t.Fatalf("failed outcome append must require fresh session state: %s", message)
			}

			restarted := newTestServer(t, nil, env.graphDir, env.sessionsDir)
			client = connect(t, restarted.srv)
			var resumed mcpserver.ResumeSessionResult
			call(t, client, "resume_session", map[string]any{"session": session}, &resumed)
			pending := resumed.PendingOperation
			if pending == nil || pending.Instance != instance || pending.RetryRef == 0 || pending.CancelRef != pending.RetryRef || pending.Command != "newEntry" {
				t.Fatalf("resume must advertise the recorded continuation: %+v", pending)
			}
			entryID := pending.Values["entryId"]
			if entryID == "" || !strings.Contains(resumed.Instructions, pending.Instructions) {
				t.Fatalf("resume lost the known entry ID or retry instructions: %+v", resumed)
			}
			capture := openServe(t, resumed, "capture")
			if capture.PendingOperation == nil || capture.PendingOperation.RetryRef != pending.RetryRef || !strings.Contains(capture.Instructions, pending.Instructions) {
				t.Fatalf("instance serve lost its continuation: %+v", capture)
			}
			tc.expect(t, client, session, pending)
		})
	}
}

func expectRetryContinuation(t *testing.T, client *mcp.ClientSession, session string, pending *sdd.WorkflowPendingOperation) {
	t.Helper()
	instance, entryID := pending.Instance, pending.Values["entryId"]
	var serve mcpserver.ServeResult
	var resumed mcpserver.ResumeSessionResult

	call(t, client, "next", map[string]any{"session": session, "instance": pending.Instance, "retry_ref": pending.RetryRef}, &serve)
	if serve.Step != "verifySummary" || serve.PendingOperation != nil {
		t.Fatalf("retry must return the ordinary successful next position: %+v", serve)
	}
	call(t, client, "next", map[string]any{"session": session, "instance": instance, "report": map[string]any{
		"chooser": "verifySummary", "choice": "faithful", "fields": map[string]any{"fidelityNote": "matches"},
	}}, &serve)
	if serve.Status != "completed" || serve.Produced["entryId"] != entryID {
		t.Fatalf("retry changed the known entry identity: %+v", serve)
	}
	call(t, client, "resume_session", map[string]any{"session": session}, &resumed)
	if resumed.PendingOperation != nil {
		t.Fatalf("completed outcome still advertises a retry: %+v", resumed.PendingOperation)
	}
}

func expectCancelContinuation(t *testing.T, client *mcp.ClientSession, session string, pending *sdd.WorkflowPendingOperation) {
	t.Helper()
	instance, entryID := pending.Instance, pending.Values["entryId"]
	var serve mcpserver.ServeResult
	var resumed mcpserver.ResumeSessionResult
	for _, fields := range []map[string]any{
		{"report": map[string]any{}, "cancel_ref": pending.CancelRef},
		{"report": assembleReport(), "retry_ref": pending.RetryRef},
		{"retry_ref": pending.RetryRef, "cancel_ref": pending.CancelRef},
		{"report": assembleReport(), "retry_ref": pending.RetryRef, "cancel_ref": pending.CancelRef},
	} {
		fields["session"], fields["instance"] = session, instance
		if message := callExpectError(t, client, "next", fields); !strings.Contains(message, "exactly one") {
			t.Fatalf("ambiguous continuation was not rejected: %s", message)
		}
	}
	call(t, client, "next", map[string]any{"session": session, "instance": instance, "cancel_ref": pending.CancelRef}, &serve)
	if serve.Step != "playback" || serve.Cancellation == nil || serve.Cancellation.Values["entryId"] != entryID || !strings.Contains(serve.Instructions, "leaves them in place") {
		t.Fatalf("cancel response lost its return or effects: %+v", serve)
	}
	call(t, client, "resume_session", map[string]any{"session": session}, &resumed)
	if resumed.PendingOperation != nil || resumed.Cancellation == nil || resumed.Cancellation.CancelRef != pending.CancelRef || !strings.Contains(resumed.Instructions, entryID) {
		t.Fatalf("lost cancel response cannot be recovered: %+v", resumed)
	}
	serve = mcpserver.ServeResult{}
	call(t, client, "next", map[string]any{"session": session, "instance": instance, "cancel_ref": pending.CancelRef}, &serve)
	if serve.Step != "playback" || serve.PendingOperation != nil || serve.Cancellation == nil {
		t.Fatalf("repeated cancellation changed position: %+v", serve)
	}
	if message := callExpectError(t, client, "next", map[string]any{"session": session, "instance": instance, "retry_ref": pending.RetryRef}); !strings.Contains(message, "cancelled") {
		t.Fatalf("cancelled invocation became retryable: %s", message)
	}

}
