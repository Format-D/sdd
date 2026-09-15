package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdd "github.com/networkteam/sdd/pkg/application"
	pkgllm "github.com/networkteam/sdd/pkg/llm"
	localadapter "github.com/networkteam/sdd/pkg/local"
)

// collectFixture builds a runtime over throwaway stores plus a fixed clock, so
// retention can be exercised without sleeping.
type collectFixture struct {
	app         *sdd.Application
	identity    sdd.RequestIdentity
	project     sdd.ProjectID
	sessions    *localadapter.FilesystemSessionStore
	blobs       *localadapter.FilesystemStagedBlobStore
	sessionsDir string
	now         time.Time
	staged      map[sdd.SessionID]string
}

const collectSubject = "local"

func newCollectFixture(t *testing.T, configure ...func(*sdd.ApplicationOptions)) collectFixture {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	sessions, err := localadapter.NewFilesystemSessionStoreAt(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := localadapter.NewFilesystemStagedBlobStoreAt(filepath.Join(root, "staged-blobs"))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := localadapter.NewFilesystemGraphStore(localadapter.FilesystemGraphStoreOptions{
		Project: "local", GraphDir: filepath.Join(root, "graph"),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	runtime, err := sdd.NewProjectRuntime(sdd.ProjectRuntimeOptions{
		Project: sdd.ProjectRef{ID: "local"}, Graph: graph,
		LLM: pkgllm.RunnerFunc(func(context.Context, pkgllm.Request) (pkgllm.Result, error) {
			return pkgllm.Result{Identity: pkgllm.Identity{Provider: "test", Model: "test"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	options := sdd.ApplicationOptions{Access: &runtimeAccessResolver{runtime: runtime}, Sessions: sessions, StagedBlobs: blobs, Clock: sdd.ClockFunc(func() time.Time { return now })}
	for _, configure := range configure {
		configure(&options)
	}
	app, err := sdd.NewApplication(options)
	if err != nil {
		t.Fatal(err)
	}
	return collectFixture{
		app: app, sessions: sessions, blobs: blobs, sessionsDir: sessionsDir, now: now,
		staged: map[sdd.SessionID]string{}, project: "local", identity: sdd.RequestIdentity{Subject: collectSubject},
	}
}

// writeSession creates a session and, when ended is non-zero, closes it out with
// a terminal record of that act at that time.
func (f collectFixture) writeSession(t *testing.T, id sdd.SessionID, ended time.Time, act sdd.SessionEndAct) {
	t.Helper()
	attachment := sdd.Attachment{
		Subject: collectSubject, ClientName: "test",
		LastActivity: f.now.Add(-72 * time.Hour),
	}
	metadata := sdd.SessionMetadata{
		ID: id, Subject: collectSubject, Project: f.project,
		Participant: "Christopher", Attachment: &attachment, UpdatedAt: f.now.Add(-72 * time.Hour),
	}
	created, err := f.sessions.Create(t.Context(), metadata)
	if err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
	if ended.IsZero() {
		return
	}
	ending := metadata
	ending.Attachment = nil
	ending.Ended = &sdd.SessionEnd{Act: act, EndedAt: ended}
	payload, err := json.Marshal(ending.Ended)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.Append(t.Context(), id, created.Version, sdd.SessionAppend{Metadata: &ending, Events: []sdd.StoredEvent{{CodecVersion: sdd.SessionCodecVersion, Code: sdd.SessionEndedEventCode, Payload: payload}}}); err != nil {
		t.Fatalf("ending %s: %v", id, err)
	}
}

func (f collectFixture) stage(t *testing.T, id sdd.SessionID) {
	t.Helper()
	owner := sdd.SessionRef{Subject: collectSubject, Session: id}
	blob, err := f.blobs.Stage(t.Context(), owner, "evidence.md", strings.NewReader("evidence"))
	if err != nil {
		t.Fatalf("Stage(%s): %v", id, err)
	}
	f.staged[id] = blob.ID
}

func (f collectFixture) collect(t *testing.T, retention time.Duration) sdd.CollectSessionsResult {
	t.Helper()
	result, err := f.app.CollectSessions(t.Context(), sdd.CollectSessionsCmd{
		Retention: retention,
	})
	if err != nil {
		t.Fatalf("CollectSessions: %v", err)
	}
	return result
}

func (f collectFixture) listedIDs(t *testing.T) []string {
	t.Helper()
	page, err := f.sessions.List(t.Context(), sdd.SessionFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, 0, len(page.Sessions))
	for _, item := range page.Sessions {
		ids = append(ids, string(item.Metadata.ID))
	}
	slices.Sort(ids)
	return ids
}

// stagedIDs reports which known fixture resources remain readable.
func (f collectFixture) stagedIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for id, blob := range f.staged {
		reader, err := f.blobs.Open(t.Context(), sdd.SessionRef{Subject: collectSubject, Session: id}, blob)
		if err == nil {
			ids = append(ids, string(id))
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// TestCollectRemovesOnlyEndedSessionsPastRetention covers the pass's whole
// removal rule in one shape: ended and past the window goes with its blobs,
// ended but inside the window stays, and never-ended stays regardless of age.
func TestCollectRemovesOnlyEndedSessionsPastRetention(t *testing.T) {
	f := newCollectFixture(t)
	retention := 14 * 24 * time.Hour

	f.writeSession(t, "s_old", f.now.Add(-30*24*time.Hour), sdd.SessionConcluded)
	f.stage(t, "s_old")
	f.writeSession(t, "s_abandoned", f.now.Add(-30*24*time.Hour), sdd.SessionAbandoned)
	f.writeSession(t, "s_recent", f.now.Add(-1*time.Hour), sdd.SessionConcluded)
	f.stage(t, "s_recent")
	f.writeSession(t, "s_open", time.Time{}, "")
	f.stage(t, "s_open")

	result := f.collect(t, retention)

	removed := make([]string, 0, len(result.RemovedSessions))
	for _, id := range result.RemovedSessions {
		removed = append(removed, string(id))
	}
	slices.Sort(removed)
	if !slices.Equal(removed, []string{"s_abandoned", "s_old"}) {
		t.Fatalf("removed = %v, want the two ended sessions past retention", removed)
	}
	if got := f.listedIDs(t); !slices.Equal(got, []string{"s_open", "s_recent"}) {
		t.Fatalf("remaining = %v, want the in-window and never-ended sessions", got)
	}

	// The removed session's blobs go with it; the survivors keep theirs.
	if kept := f.stagedIDs(t); !slices.Equal(kept, []string{"s_open", "s_recent"}) {
		t.Fatalf("staged owners = %v, want only the surviving sessions", kept)
	}
}

// TestCollectPagesToConvergence pins the bounded shape: one pass processes a
// page of sessions and names where it stopped, repeating until Next is empty
// removes everything due, and a session the pass keeps never starves the pages
// after it.
func TestCollectPagesToConvergence(t *testing.T) {
	f := newCollectFixture(t)
	for _, id := range []sdd.SessionID{"s_a", "s_b", "s_c", "s_d", "s_e"} {
		f.writeSession(t, id, f.now.Add(-30*24*time.Hour), sdd.SessionConcluded)
		f.stage(t, id)
	}
	f.writeSession(t, "s_live", time.Time{}, "")
	f.stage(t, "s_live")

	var removed []string
	passes := 0
	cmd := sdd.CollectSessionsCmd{Retention: time.Hour, Limit: 2}
	for {
		result, err := f.app.CollectSessions(t.Context(), cmd)
		if err != nil {
			t.Fatalf("CollectSessions after %q: %v", cmd.After, err)
		}
		passes++
		if len(result.RemovedSessions) > cmd.Limit {
			t.Fatalf("pass removed %d sessions, want at most the Limit of %d", len(result.RemovedSessions), cmd.Limit)
		}
		for _, id := range result.RemovedSessions {
			removed = append(removed, string(id))
		}
		if result.Next == "" {
			break
		}
		if result.Next <= cmd.After {
			t.Fatalf("Next %q did not advance past %q", result.Next, cmd.After)
		}
		cmd.After = result.Next
	}
	slices.Sort(removed)
	if !slices.Equal(removed, []string{"s_a", "s_b", "s_c", "s_d", "s_e"}) {
		t.Fatalf("removed = %v over %d passes, want every ended session", removed, passes)
	}
	if passes < 3 {
		t.Fatalf("converged in %d passes, want the Limit of 2 to need at least 3", passes)
	}
	if got := f.listedIDs(t); !slices.Equal(got, []string{"s_live"}) {
		t.Fatalf("remaining sessions = %v, want only the live one", got)
	}
	if got := f.stagedIDs(t); !slices.Equal(got, []string{"s_live"}) {
		t.Fatalf("remaining staged = %v, want only the live session's", got)
	}
}

// TestCollectIsIdempotent pins the property that replaces all
// coordination: the target set is recomputed each run, so a repeat finds nothing
// to do and never errors on work already done.
func TestCollectIsIdempotent(t *testing.T) {
	f := newCollectFixture(t)
	f.writeSession(t, "s_old", f.now.Add(-30*24*time.Hour), sdd.SessionConcluded)
	f.stage(t, "s_old")

	first := f.collect(t, time.Hour)
	if len(first.RemovedSessions) != 1 {
		t.Fatalf("first pass removed %d, want 1", len(first.RemovedSessions))
	}
	second := f.collect(t, time.Hour)
	if len(second.RemovedSessions) != 0 || len(second.RemovedStaged) != 0 {
		t.Fatalf("second pass removed %+v, want nothing left to do", second)
	}
}

// TestCollectLeavesUnreadableSessionsAlone is the criterion that an unreadable
// log is never treated as garbage: it may belong to a newer binary.
func TestCollectLeavesUnreadableSessionsAlone(t *testing.T) {
	f := newCollectFixture(t)
	f.writeSession(t, "s_old", f.now.Add(-30*24*time.Hour), sdd.SessionConcluded)

	const corrupt = "{not json at all\n"
	unreadable := filepath.Join(f.sessionsDir, "s_unreadable.jsonl")
	if err := os.WriteFile(unreadable, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	f.collect(t, time.Hour)

	after, err := os.ReadFile(unreadable)
	if err != nil {
		t.Fatalf("the unreadable log was removed; it may belong to a newer binary: %v", err)
	}
	if string(after) != corrupt {
		t.Fatalf("the unreadable log was modified: %q", after)
	}
}

type failingResourceDeletion struct {
	sdd.StagedBlobStore
	err error
}

func (s *failingResourceDeletion) DeleteStaged(ctx context.Context, ref sdd.SessionRef) error {
	if s.err != nil {
		return s.err
	}
	return s.StagedBlobStore.DeleteStaged(ctx, ref)
}

type failingSessionDeletion struct {
	sdd.SessionStore
	err error
}

func (s *failingSessionDeletion) Delete(ctx context.Context, id sdd.SessionID) error {
	if s.err != nil {
		return s.err
	}
	return s.SessionStore.Delete(ctx, id)
}

func TestCollectRetriesResourceAndSessionDeletion(t *testing.T) {
	for _, failure := range []string{"resource deletion", "session deletion"} {
		t.Run(failure, func(t *testing.T) {
			injected := errors.New("injected cleanup failure")
			var resources *failingResourceDeletion
			var sessions *failingSessionDeletion
			f := newCollectFixture(t, func(options *sdd.ApplicationOptions) {
				resources = &failingResourceDeletion{StagedBlobStore: options.StagedBlobs}
				sessions = &failingSessionDeletion{SessionStore: options.Sessions}
				options.StagedBlobs, options.Sessions = resources, sessions
			})
			f.writeSession(t, "s_old", f.now.Add(-30*24*time.Hour), sdd.SessionConcluded)
			f.stage(t, "s_old")
			if failure == "resource deletion" {
				resources.err = injected
			} else {
				sessions.err = injected
			}

			_, err := f.app.CollectSessions(t.Context(), sdd.CollectSessionsCmd{Retention: time.Hour})
			if !errors.Is(err, injected) {
				t.Fatalf("CollectSessions error = %v", err)
			}
			if got := f.listedIDs(t); !slices.Equal(got, []string{"s_old"}) {
				t.Fatalf("session lost after cleanup failure: %v", got)
			}
			if got := f.stagedIDs(t); (len(got) == 1) != (failure == "resource deletion") {
				t.Fatalf("remaining resources = %v after %s", got, failure)
			}

			resources.err, sessions.err = nil, nil
			result := f.collect(t, time.Hour)
			if !slices.Equal(result.RemovedSessions, []sdd.SessionID{"s_old"}) {
				t.Fatalf("retry removed %v", result.RemovedSessions)
			}
			if got := f.stagedIDs(t); len(got) != 0 {
				t.Fatalf("resources remain after retry: %v", got)
			}
		})
	}
}

func TestCollectDoesNotExecutePendingWrites(t *testing.T) {
	f := newCollectFixture(t)
	f.writeSession(t, "s_ended", f.now.Add(-30*24*time.Hour), sdd.SessionAbandoned)
	f.writeSession(t, "s_open", time.Time{}, "")
	for _, id := range []sdd.SessionID{"s_ended", "s_open"} {
		stored, err := f.sessions.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.sessions.Append(t.Context(), id, stored.Version, sdd.SessionAppend{Events: []sdd.StoredEvent{{CodecVersion: sdd.SessionCodecVersion, Code: "mutation_intent", Payload: json.RawMessage(`{"legacy":"unroutable"}`)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := f.sessions.Load(t.Context(), "s_open")
	if err != nil {
		t.Fatal(err)
	}
	result := f.collect(t, time.Hour)
	if !slices.Equal(result.RemovedSessions, []sdd.SessionID{"s_ended"}) {
		t.Fatalf("removed = %v", result.RemovedSessions)
	}
	after, err := f.sessions.Load(t.Context(), "s_open")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version {
		t.Fatalf("collector appended to pending open session: %d -> %d", before.Version, after.Version)
	}
}
