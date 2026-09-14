package proctest_test

import (
	"strings"
	"testing"

	"github.com/networkteam/sdd/internal/proctest"
	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestCapture_CancellationLeavesPublishedEffectsAndAllowsFreshConfirmation(t *testing.T) {
	finalizer := &captureCompletionFailure{}
	world, session := newCaptureWorld(t, "cancel-capture", proctest.WithFinalizers(finalizer))
	other := session.Start(t, "capture", nil)
	serve := session.Start(t, "capture", nil)
	instance := serve.Instance
	handle := session.Stage(t, "record.md", []byte("The immutable supporting record."))
	draft := captureDraft()
	draft["attachments"] = []any{handle}
	session.Report(t, instance, draft)
	_, failure := session.AnswerErr(t, instance, "playback", "confirm", nil, "publish the observation")
	if failure == nil || !strings.Contains(failure.Error(), "retry_ref=") || !strings.Contains(failure.Error(), "cancel_ref=") || !strings.Contains(failure.Error(), "retry reuses it") {
		t.Fatalf("failure must explain both continuations: %v", failure)
	}
	session, resumed := world.Resume(t, session.ID, "cancel-capture-resumed")
	pending := resumed.PendingOperation
	if pending == nil || pending.CancelRef != pending.RetryRef || pending.Instance != instance {
		t.Fatalf("resume lost the exact continuations: %+v", pending)
	}
	originalID := pending.Values["entryId"]
	proctest.LoadEntry(t, world.GraphDir, originalID)
	requirePendingSessionBlocksProgression(t, session, other.Instance)
	serve, err := session.WF.Advance(t.Context(), world.Identity, sdd.WorkflowAdvanceRequest{Instance: instance, CancelRef: pending.CancelRef})
	if err != nil {
		t.Fatal(err)
	}
	proctest.RequireStep(t, serve, "playback")
	if serve.Cancellation == nil || serve.Cancellation.Values["entryId"] != originalID || !strings.Contains(serve.Cancellation.Instructions, "leaves them in place") || finalizer.calls != 1 {
		t.Fatalf("cancellation must report residue without completing it: %+v, finalizers=%d", serve.Cancellation, finalizer.calls)
	}
	requireListedStep(t, session, instance, "playback")
	session, resumed = world.Resume(t, session.ID, "cancel-response-lost")
	if resumed.PendingOperation != nil || resumed.Cancellation == nil || resumed.Cancellation.Values["entryId"] != originalID {
		t.Fatalf("resume lost durable cancellation: %+v", resumed)
	}
	if _, err := session.WF.Advance(t.Context(), world.Identity, sdd.WorkflowAdvanceRequest{Instance: instance, RetryRef: pending.RetryRef}); err == nil || finalizer.calls != 1 {
		t.Fatalf("cancelled retry executed: %v, finalizers=%d", err, finalizer.calls)
	}
	attachment, err := world.App.ReadAttachment(t.Context(), world.Identity, "proctest", sdd.ReadAttachmentRequest{EntryID: originalID, Filename: handle, MaxBytes: 1024})
	if err != nil || string(attachment.Page.Content) != "The immutable supporting record." {
		t.Fatalf("cancel removed published attachment: %+v, %v", attachment, err)
	}
	serve = session.Answer(t, instance, "playback", "confirm", nil, "publish a fresh observation")
	proctest.RequireStep(t, serve, "verifySummary")
	serve = session.Answer(t, instance, "verifySummary", "faithful", map[string]any{"fidelityNote": "matches"}, "")
	freshID, _ := serve.Produced["entryId"].(string)
	if freshID == "" || freshID == originalID || finalizer.calls != 2 || world.LLM.Calls("summarize") != 2 {
		t.Fatalf("fresh confirmation reused cancelled work: original=%s fresh=%s finalizers=%d summaries=%d", originalID, freshID, finalizer.calls, world.LLM.Calls("summarize"))
	}
	proctest.LoadEntry(t, world.GraphDir, originalID)
	proctest.LoadEntry(t, world.GraphDir, freshID)
	if _, err := session.WF.Advance(t.Context(), world.Identity, sdd.WorkflowAdvanceRequest{Instance: instance, RetryRef: pending.RetryRef}); err == nil || finalizer.calls != 2 {
		t.Fatalf("old retry executed after fresh publication: %v", err)
	}
	if _, err := world.App.AbandonWorkflowSession(t.Context(), world.Identity, sdd.WorkflowResumeRequest{SessionID: session.ID}, "resolved the operation first"); err != nil {
		t.Fatalf("resolved session must allow ending: %v", err)
	}
}
