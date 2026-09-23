package proctest_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/internal/proctest"
	sdd "github.com/networkteam/sdd/pkg/application"
)

// failingFinalizer fails the n-th completion it is asked for and succeeds on
// every other call, so one publication's completion can be interrupted while
// the ones before it land.
type failingFinalizer struct {
	failCall int
	calls    int
}

func (*failingFinalizer) Name() string { return "interrupted-completion" }
func (f *failingFinalizer) Finalize(context.Context, sdd.AppliedMutation) error {
	f.calls++
	if f.calls == f.failCall {
		return errors.New("completion unavailable after publication")
	}
	return nil
}

// A summary correction is a recorded write like capture: when its completion
// fails, the position is served pending; the retry publishes nothing twice and
// completes the transition the verifySummary answer owed (s-tac-do6), and the
// corrected summary is what the entry carries.
func TestCapture_SummaryCorrectionRetryCompletesTheAnsweredTransition(t *testing.T) {
	finalizer := &failingFinalizer{failCall: 2}
	world, session := newCaptureWorld(t, "summary-correction", proctest.WithFinalizers(finalizer))
	before := entryIDsOnDisk(t, world.GraphDir)
	serve := session.Start(t, "capture", nil)
	instance := serve.Instance
	session.Report(t, instance, captureDraft())
	serve = session.Answer(t, instance, "playback", "confirm", nil, "publish the observation")
	proctest.RequireStep(t, serve, "verifySummary")
	entryID := writtenEntryID(t, world.GraphDir, before)

	failed, err := session.AnswerErr(t, instance, "verifySummary", "drifted", map[string]any{"correctedSummary": "The corrected summary."}, "")
	if err != nil {
		t.Fatal(err)
	}
	if failed.PendingOperation == nil || failed.PendingOperation.Command != "replaceSummary" || failed.PendingOperation.Values["entryId"] != entryID {
		t.Fatalf("a failed summary correction must serve its pending position: %+v", failed.PendingOperation)
	}
	if got := proctest.LoadEntry(t, world.GraphDir, entryID).Summary; got != "The corrected summary." {
		t.Fatalf("the replacement was written before its completion failed; summary = %q", got)
	}
	if finalizer.calls != 2 {
		t.Fatalf("finalizer calls = %d, want 2 (capture, then the failed correction)", finalizer.calls)
	}

	retried, err := session.WF.Advance(t.Context(), world.Identity, sdd.WorkflowAdvanceRequest{Instance: instance, RetryRef: failed.PendingOperation.RetryRef})
	if err != nil {
		t.Fatal(err)
	}
	proctest.RequireStatus(t, retried, "completed")
	if finalizer.calls != 3 {
		t.Fatalf("retry must complete the publication once more, calls = %d", finalizer.calls)
	}
	if got := proctest.LoadEntry(t, world.GraphDir, entryID).Summary; got != "The corrected summary." {
		t.Fatalf("summary after retry = %q", got)
	}
	if world.LLM.Calls("summarize") != 1 {
		t.Fatalf("a correction must not regenerate the summary; summarize calls = %d", world.LLM.Calls("summarize"))
	}
}

// A summary correction conditions on the document it was read from: a
// document that moved meanwhile is refused as a conflict naming the current
// summary, and nothing is written.
func TestApplication_SummaryCorrectionRefusesAMovedDocument(t *testing.T) {
	world, session := newCaptureWorld(t, "summary-conflict")
	before := entryIDsOnDisk(t, world.GraphDir)
	serve := session.Start(t, "capture", nil)
	instance := serve.Instance
	session.Report(t, instance, captureDraft())
	serve = session.Answer(t, instance, "playback", "confirm", nil, "publish the observation")
	proctest.RequireStep(t, serve, "verifySummary")
	entryID := writtenEntryID(t, world.GraphDir, before)
	binding := session.WF.Binding()

	_, err := world.App.ReplaceSummary(t.Context(), world.Identity, "proctest", binding, sdd.SummaryReplacement{
		Publication:  sdd.PublicationKey{Session: session.ID, Sequence: 99, Discriminator: "replaceSummary:" + entryID},
		EntryID:      entryID,
		ExpectedBlob: sdd.GitBlobID([]byte("the document as some earlier read saw it")),
		Summary:      "A late correction.",
	})
	var appErr *sdd.ApplicationError
	if !errors.As(err, &appErr) || appErr.Code != sdd.ErrorGraphConflict {
		t.Fatalf("stale correction = %v, want %s", err, sdd.ErrorGraphConflict)
	}
	current := proctest.LoadEntry(t, world.GraphDir, entryID).Summary
	if current != "A generated summary." || !containsAll(appErr.Message, current) {
		t.Fatalf("conflict must leave the summary and name it: summary=%q message=%q", current, appErr.Message)
	}
}

// A WIP marker's identity is allocated before the intent, so a retry after a
// failed completion publishes the same marker once and lands on the step the
// setup answer owed.
func TestImplementation_WIPStartRetryPublishesOneMarker(t *testing.T) {
	finalizer := &failingFinalizer{failCall: 1}
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()), proctest.WithFinalizers(finalizer))
	session := world.Open(t, "wip-retry")
	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID}, "main")
	failed, err := session.AnswerErr(t, serve.Instance, "setup", "inPlace", map[string]any{"wipDescription": "implement the anchor"}, "in place")
	if err != nil {
		t.Fatal(err)
	}
	if failed.PendingOperation == nil || failed.PendingOperation.Command != "wipStart" {
		t.Fatalf("a failed marker publication must serve its pending position: %+v", failed.PendingOperation)
	}
	marker := requireSingleMarker(t, world.GraphDir)
	if marker.ID != failed.PendingOperation.Values["markerId"] {
		t.Fatalf("marker on disk %s, intent recorded %s", marker.ID, failed.PendingOperation.Values["markerId"])
	}
	retried, err := session.WF.Advance(t.Context(), world.Identity, sdd.WorkflowAdvanceRequest{Instance: serve.Instance, RetryRef: failed.PendingOperation.RetryRef})
	if err != nil {
		t.Fatal(err)
	}
	proctest.RequireStep(t, retried, "workTarget")
	if again := requireSingleMarker(t, world.GraphDir); again.ID != marker.ID {
		t.Fatalf("retry published another marker: %s then %s", marker.ID, again.ID)
	}
	if finalizer.calls != 2 {
		t.Fatalf("finalizer calls = %d, want the failed and the completing one", finalizer.calls)
	}
}

// Removing a marker that is already gone succeeds: the landing still closes
// the run, and no other marker is touched.
func TestImplementation_LandingRemovesAnAbsentMarkerWithoutError(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "wip-absent")
	serve := startImplementationAtWork(t, session)
	instance := serve.Instance
	marker := requireSingleMarker(t, world.GraphDir)
	other := writeWIPMarker(t, world.GraphDir, "20260601-130000-someone-else", implAnchorID)
	if err := os.Remove(filepath.Join(world.GraphDir, "wip", marker.ID+".md")); err != nil {
		t.Fatal(err)
	}

	serve = session.Answer(t, instance, "work", "conclude", nil, "done")
	proctest.RequireStep(t, serve, "record")
	doneID := captureDone(t, session, instance)
	serve = session.Report(t, instance, map[string]any{"doneEntry": doneID})
	proctest.RequireStep(t, serve, "landing")
	serve = session.Answer(t, instance, "landing", "landed", nil, "merged")
	proctest.RequireStep(t, serve, "closeout")
	if ids := wipMarkerIDs(t, world.GraphDir); len(ids) != 1 || ids[0] != "20260601-130000-someone-else" {
		t.Fatalf("markers after landing = %v, want only the other participant's", ids)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another participant's marker was removed: %v", err)
	}
}

// entryIDsOnDisk lists the entries a graph directory holds.
func entryIDsOnDisk(t *testing.T, graphDir string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	err := filepath.WalkDir(graphDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "wip" || entry.Name() == ".sdd-runtime" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(graphDir, name)
		if err != nil {
			return err
		}
		if id, err := model.RelPathToID(filepath.ToSlash(rel)); err == nil {
			ids[id] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// writtenEntryID returns the one entry written since before was taken.
func writtenEntryID(t *testing.T, graphDir string, before map[string]bool) string {
	t.Helper()
	var found string
	for id := range entryIDsOnDisk(t, graphDir) {
		if before[id] {
			continue
		}
		if found != "" {
			t.Fatalf("more than one written entry: %s and %s", found, id)
		}
		found = id
	}
	if found == "" {
		t.Fatal("no entry was written")
	}
	return found
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		found := false
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
