package application_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestRefreshWorkflowPreservesAttachmentAndServedMemory(t *testing.T) {
	application, sessions, _, _ := newStampWorkflowApp(t, "", "", time.Now)
	identity := sdd.RequestIdentity{Subject: "christopher"}
	workflow, _, err := application.OpenWorkflow(t.Context(), identity, "example", sdd.WorkflowOpenRequest{ClientName: "original-client"})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.RecordServed(t.Context(), identity, []string{"already-delivered"}); err != nil {
		t.Fatal(err)
	}
	before, err := sessions.Load(t.Context(), workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := application.RefreshWorkflow(t.Context(), identity, workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	if replay == workflow || !replay.ServedBefore("already-delivered") {
		t.Fatal("refresh did not return an independent replay of served memory")
	}
	after, err := sessions.Load(t.Context(), workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refresh changed the durable session")
	}
	if _, err := application.RefreshWorkflow(t.Context(), sdd.RequestIdentity{Subject: "other"}, workflow.ID()); err == nil {
		t.Fatal("refresh bypassed session authorization")
	}
	if _, err := application.AbandonWorkflowSession(t.Context(), identity, sdd.WorkflowResumeRequest{SessionID: workflow.ID()}, "done"); err != nil {
		t.Fatal(err)
	}
	_, err = application.RefreshWorkflow(t.Context(), identity, workflow.ID())
	var appErr *sdd.ApplicationError
	if !errors.As(err, &appErr) || appErr.Code != sdd.ErrorSessionEnded {
		t.Fatalf("refresh after ending = %v", err)
	}
}

type rejectNextWorkflowAppend struct {
	sdd.SessionStore
	armed    atomic.Bool
	rejected atomic.Int64
}

func (s *rejectNextWorkflowAppend) Append(ctx context.Context, id sdd.SessionID, version uint64, data sdd.SessionAppend) (uint64, error) {
	if s.armed.CompareAndSwap(true, false) {
		s.rejected.Add(1)
		return version, &sdd.ApplicationError{Code: sdd.ErrorSessionConflict, Message: "session version changed"}
	}
	return s.SessionStore.Append(ctx, id, version, data)
}

func TestRefreshWorkflowDiscardsFailedAppendAtUnchangedVersion(t *testing.T) {
	rejected := &rejectNextWorkflowAppend{}
	application, sessions, _, _ := newStampWorkflowApp(t, "", "", time.Now, func(store sdd.SessionStore) sdd.SessionStore { rejected.SessionStore = store; return rejected })
	identity := sdd.RequestIdentity{Subject: "christopher"}
	workflow, _, err := application.OpenWorkflow(t.Context(), identity, "example", sdd.WorkflowOpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	serve, err := workflow.Start(t.Context(), identity, sdd.WorkflowStartRequest{Canonical: "waiting-test"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := sessions.Load(t.Context(), workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	rejected.armed.Store(true)
	_, err = workflow.Advance(t.Context(), identity, sdd.WorkflowAdvanceRequest{Instance: serve.Instance, Report: map[string]any{"body": "rejected"}})
	var appErr *sdd.ApplicationError
	if !errors.As(err, &appErr) || appErr.Code != sdd.ErrorSessionConflict {
		t.Fatalf("append rejection = %v", err)
	}
	after, err := sessions.Load(t.Context(), workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || rejected.rejected.Load() != 1 {
		t.Fatal("rejected append was retried or persisted")
	}
	workflow, err = application.RefreshWorkflow(t.Context(), identity, workflow.ID())
	if err != nil {
		t.Fatal(err)
	}
	position, err := workflow.ServeAll(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, open := range position.Open {
		if open.Instance == serve.Instance && open.Collected["body"] != nil {
			t.Fatalf("refresh kept rejected report: %+v", open.Collected)
		}
	}
	if _, err := workflow.Advance(t.Context(), identity, sdd.WorkflowAdvanceRequest{Instance: serve.Instance, Report: map[string]any{"body": "accepted"}}); err != nil {
		t.Fatal(err)
	}
}
