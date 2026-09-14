package mcpapp_test

import (
	"strings"
	"testing"

	"github.com/networkteam/sdd/internal/engine"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

func TestResumeAdvertisesPendingOperationAfterLostOutcome(t *testing.T) {
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
	if !strings.Contains(message, "session event append unavailable") || strings.Contains(message, "retry_ref=") {
		t.Fatalf("failed outcome append must require fresh session state: %s", message)
	}

	restarted := newTestServer(t, nil, env.graphDir, env.sessionsDir)
	client = connect(t, restarted.srv)
	var resumed mcpserver.ResumeSessionResult
	call(t, client, "resume_session", map[string]any{"session": session}, &resumed)
	pending := resumed.PendingOperation
	if pending == nil || pending.Instance != instance || pending.RetryRef == 0 || pending.Command != "newEntry" {
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
