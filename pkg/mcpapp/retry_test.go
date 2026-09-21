package mcpapp_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdd "github.com/networkteam/sdd/pkg/application"

	"github.com/networkteam/sdd/internal/engine"
	localadapter "github.com/networkteam/sdd/pkg/local"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

// failingPublisher fails the publication itself while armed and delegates
// otherwise, so nothing before the write is touched by the fault.
type failingPublisher struct {
	*localadapter.FilesystemGraphStore
	armed atomic.Bool
}

func (p *failingPublisher) PublishEntry(ctx context.Context, key sdd.PublicationKey, batch sdd.MutationBatch, blobs sdd.StagedBlobReader) (sdd.EntryPublication, error) {
	if p.armed.Load() {
		return sdd.EntryPublication{}, errors.New("publication store is temporarily unavailable")
	}
	return p.FilesystemGraphStore.PublishEntry(ctx, key, batch, blobs)
}

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

// operationFixture is one capture at playback over a server whose clock and
// entry suffix are fixed, so the identifiers in a response agree with each
// other across snapshots (d-cpt-t8i).
type operationFixture struct {
	env       testEnv
	client    *mcp.ClientSession
	session   string
	instance  string
	publisher *failingPublisher
}

func deterministicIdentity(opts *testServerOptions) {
	opts.Application.Clock = sdd.ClockFunc(func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) })
	opts.Application.EntrySuffix = func(int) (string, error) { return "op1", nil }
}

func newOperationFixture(t *testing.T, mutate ...func(*testServerOptions)) *operationFixture {
	t.Helper()
	mutate = append([]func(*testServerOptions){deterministicIdentity}, mutate...)
	env := newTestServer(t, nil, "", "", mutate...)
	store, ok := env.targets.fallback.(*localadapter.FilesystemGraphStore)
	if !ok {
		t.Fatalf("fixture graph store is %T, want the filesystem store", env.targets.fallback)
	}
	publisher := &failingPublisher{FilesystemGraphStore: store}
	env.targets.set("main", publisher)
	client := connect(t, env.srv)
	session := openSession(t, client).Session
	var serve mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": session, "canonical": "capture"}, &serve)
	call(t, client, "next", map[string]any{"session": session, "instance": serve.Instance, "report": assembleReport()}, &serve)
	if serve.Step != "playback" {
		t.Fatalf("capture must stand at playback, got %s", serve.Step)
	}
	return &operationFixture{env: env, client: client, session: session, instance: serve.Instance, publisher: publisher}
}

// failPublication makes the publication fail before anything is written: the
// intent is recorded, the operation is not.
func (f *operationFixture) failPublication(t *testing.T) {
	t.Helper()
	f.publisher.armed.Store(true)
}

func (f *operationFixture) clearFault() { f.publisher.armed.Store(false) }

func (f *operationFixture) confirm(t *testing.T) *mcp.CallToolResult {
	t.Helper()
	return rawCall(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "report": map[string]any{
		"chooser": "playback", "choice": "confirm", "userWords": "capture it",
	}})
}

// restart re-hosts the session on a fresh server, the lost-reply scenario.
func (f *operationFixture) restart(t *testing.T) {
	t.Helper()
	f.env = newTestServer(t, nil, f.env.graphDir, f.env.sessionsDir, deterministicIdentity)
	f.client = connect(t, f.env.srv)
}

func rawCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func TestFailedOperationServesPendingPositionWithLiveError(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	res := f.confirm(t)
	if res.IsError {
		t.Fatalf("a failed operation is a position, not an error: %s", contentText(res))
	}
	var serve mcpserver.ServeResult
	decodeStructured(t, res, &serve)
	pending := serve.PendingOperation
	if pending == nil || pending.RetryRef == 0 || pending.CancelRef != pending.RetryRef || pending.Command != "newEntry" || pending.Error == "" {
		t.Fatalf("pending operation must carry its refs, command and live error: %+v", pending)
	}
	if serve.Status != "running" || serve.Step != "write" || serve.Goal == "" || serve.Instructions == "" {
		t.Fatalf("pending position must stay a running position with its own goal and prose: %+v", serve)
	}
	if serve.ReportSchema != nil || len(serve.Missing) != 0 || serve.PendingChooser != nil {
		t.Fatalf("pending position must invite nothing but retry or cancel: %+v", serve)
	}
	snapshotResponse(t, res)
}

func TestReportWhileOperationPendingIsRefusedAndReadsStayOpen(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	if res := f.confirm(t); res.IsError {
		t.Fatalf("setup: %s", contentText(res))
	}
	res := rawCall(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "report": assembleReport()})
	if !res.IsError {
		t.Fatal("a report while an operation is unfinished must be refused")
	}
	snapshotResponse(t, res)
	var shown map[string]any
	call(t, f.client, "show", map[string]any{"session": f.session, "ids": []string{fixtureGapID}}, &shown)
}

func TestResumeServesPendingPositionWithoutLiveError(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	var failed mcpserver.ServeResult
	decodeStructured(t, f.confirm(t), &failed)
	f.restart(t)
	res := rawCall(t, f.client, "resume_session", map[string]any{"session": f.session})
	checkToolCall(t, "resume_session", res, nil)
	var resumed mcpserver.ResumeSessionResult
	decodeStructured(t, res, &resumed)
	pending := resumed.PendingOperation
	if pending == nil || pending.RetryRef != failed.PendingOperation.RetryRef || pending.Error != "" {
		t.Fatalf("resume must carry the recorded continuation without the live error: %+v", pending)
	}
	capture := openServe(t, resumed, "capture")
	if capture.PendingOperation == nil || capture.ReportSchema != nil || capture.Instructions == "" {
		t.Fatalf("the pending instance must resume at its pending position: %+v", capture)
	}
	if strings.Contains(resumed.Instructions, capture.Instructions) {
		t.Fatal("the pending prose must arrive once, on the instance's serve")
	}
	snapshotResponse(t, res)
}

func TestRetryAfterFaultClearsReturnsNormalPosition(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	var failed mcpserver.ServeResult
	decodeStructured(t, f.confirm(t), &failed)
	f.clearFault()
	var serve mcpserver.ServeResult
	call(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "retry_ref": failed.PendingOperation.RetryRef}, &serve)
	if serve.Step != "verifySummary" || serve.PendingOperation != nil {
		t.Fatalf("retry must return the ordinary successful position: %+v", serve)
	}
	call(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "report": map[string]any{
		"chooser": "verifySummary", "choice": "faithful", "fields": map[string]any{"fidelityNote": "matches"},
	}}, &serve)
	if serve.Status != "completed" || serve.Produced["entryId"] != failed.PendingOperation.Values["entryId"] {
		t.Fatalf("retry changed the recorded entry identity: %+v", serve)
	}
	var resumed mcpserver.ResumeSessionResult
	call(t, f.client, "resume_session", map[string]any{"session": f.session}, &resumed)
	if resumed.PendingOperation != nil {
		t.Fatalf("completed outcome still advertises a retry: %+v", resumed.PendingOperation)
	}
}

func TestNextRequiresExactlyOneContinuationInput(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	var failed mcpserver.ServeResult
	decodeStructured(t, f.confirm(t), &failed)
	ref := failed.PendingOperation.RetryRef
	for _, fields := range []map[string]any{
		{"report": map[string]any{}, "cancel_ref": ref},
		{"report": assembleReport(), "retry_ref": ref},
		{"retry_ref": ref, "cancel_ref": ref},
	} {
		fields["session"], fields["instance"] = f.session, f.instance
		if res := rawCall(t, f.client, "next", fields); !res.IsError {
			t.Fatalf("ambiguous continuation was accepted: %v", fields)
		}
	}
}

func TestCancellationReturnsToPlaybackAndReportsAbsentEntry(t *testing.T) {
	f := newOperationFixture(t)
	f.failPublication(t)
	var failed mcpserver.ServeResult
	decodeStructured(t, f.confirm(t), &failed)
	f.clearFault()
	res := rawCall(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "cancel_ref": failed.PendingOperation.CancelRef})
	checkToolCall(t, "next", res, nil)
	var serve mcpserver.ServeResult
	decodeStructured(t, res, &serve)
	cancelled := serve.Cancellation
	if serve.Step != "playback" || serve.PendingChooser == nil || serve.PendingOperation != nil || cancelled == nil {
		t.Fatalf("cancellation must land on the live playback step: %+v", serve)
	}
	want := []mcpserver.EffectResult{{Kind: "entry", ID: failed.PendingOperation.Values["entryId"], State: "absent"}}
	if cancelled.ReturnStep != "playback" || cancelled.Closed || len(cancelled.Effects) != 1 || cancelled.Effects[0] != want[0] {
		t.Fatalf("cancellation must report the operation's effects: %+v", cancelled)
	}
	snapshotResponse(t, res)

	var again mcpserver.ServeResult
	call(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "cancel_ref": failed.PendingOperation.CancelRef}, &again)
	if again.Step != "playback" || again.Cancellation == nil {
		t.Fatalf("repeated cancellation changed position: %+v", again)
	}
	if res := rawCall(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "retry_ref": failed.PendingOperation.RetryRef}); !res.IsError {
		t.Fatal("a cancelled operation became retryable")
	}
	var fresh mcpserver.ServeResult
	decodeStructured(t, f.confirm(t), &fresh)
	if fresh.Step != "verifySummary" || fresh.Cancellation != nil {
		t.Fatalf("fresh confirmation must start a new operation: %+v", fresh)
	}
}

func TestCancellationAfterLostOutcomeReportsPublishedEntry(t *testing.T) {
	store := &sessionStoreProbe{}
	f := newOperationFixture(t, func(opts *testServerOptions) {
		store.SessionStore = opts.Application.Sessions
		opts.Application.Sessions = store
	})
	store.FailNextEvent(engine.EventMutationOutcome)
	if res := f.confirm(t); !res.IsError {
		t.Fatal("a lost outcome append must fail the request")
	}
	f.restart(t)
	var resumed mcpserver.ResumeSessionResult
	call(t, f.client, "resume_session", map[string]any{"session": f.session}, &resumed)
	if resumed.PendingOperation == nil {
		t.Fatalf("resume must advertise the recorded continuation: %+v", resumed)
	}
	res := rawCall(t, f.client, "next", map[string]any{"session": f.session, "instance": f.instance, "cancel_ref": resumed.PendingOperation.CancelRef})
	checkToolCall(t, "next", res, nil)
	var serve mcpserver.ServeResult
	decodeStructured(t, res, &serve)
	want := mcpserver.EffectResult{Kind: "entry", ID: resumed.PendingOperation.Values["entryId"], State: "published"}
	if serve.Cancellation == nil || len(serve.Cancellation.Effects) != 1 || serve.Cancellation.Effects[0] != want {
		t.Fatalf("cancellation must report the entry the operation published: %+v", serve.Cancellation)
	}
	snapshotResponse(t, res)
}
