package proctest_test

// Ported from internal/engine/implementation_test.go: the same shipped
// implementation entry, driven through the real application. Fake WIP markers
// became real marker files under each store's wip/ directory, the fake branch
// router became WithBranchDir stores, and the closing done is recorded through
// the real dispatched capture procedure.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/internal/proctest"
	sdd "github.com/networkteam/sdd/pkg/application"
)

const (
	implAnchorID = "20260601-120000-d-tac-ref"
	implDoneID   = "20260601-130000-s-tac-don"
	// implMissingID is well-formed but absent from every fixture store.
	implMissingID = "20260601-140000-d-tac-gon"
)

func implAnchorEntry() *model.Entry {
	return &model.Entry{
		ID: implAnchorID, Type: model.TypeDecision, Kind: model.KindDirective, Layer: model.LayerTactical, Intent: model.IntentPending,
		Summary: "A directive the implementation anchors on.",
		Content: "A directive the implementation anchors on.",
	}
}

// implDoneEntry is a pre-recorded closing done fixture for scenarios that
// exercise routing rather than the capture sub-move itself.
func implDoneEntry() *model.Entry {
	return &model.Entry{
		ID: implDoneID, Type: model.TypeSignal, Kind: model.KindDone, Layer: model.LayerTactical,
		Closes:  []string{implAnchorID},
		Summary: "The anchor directive was delivered in commit abc1234.",
		Content: "The anchor directive was delivered in commit abc1234.",
	}
}

// startImplChild starts a procedure under a parent instance, riding the
// parent's dispatch seed — harness Session.Start always parents on the shell.
func startImplChild(t *testing.T, session *proctest.Session, canonical, parent string) *sdd.WorkflowServe {
	t.Helper()
	serve, err := session.WF.Start(t.Context(), session.World.Identity, sdd.WorkflowStartRequest{Canonical: canonical, Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	return serve
}

func wipMarkerIDs(t *testing.T, graphDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(model.WIPDir(graphDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			ids = append(ids, strings.TrimSuffix(entry.Name(), ".md"))
		}
	}
	return ids
}

func requireNoMarkers(t *testing.T, graphDir string) {
	t.Helper()
	if ids := wipMarkerIDs(t, graphDir); len(ids) != 0 {
		t.Fatalf("wip markers in %s = %v, want none", graphDir, ids)
	}
}

func requireSingleMarker(t *testing.T, graphDir string) *model.WIPMarker {
	t.Helper()
	ids := wipMarkerIDs(t, graphDir)
	if len(ids) != 1 {
		t.Fatalf("wip markers in %s = %v, want exactly one", graphDir, ids)
	}
	content, err := os.ReadFile(filepath.Join(model.WIPDir(graphDir), ids[0]+".md"))
	if err != nil {
		t.Fatal(err)
	}
	marker, err := model.ParseWIPMarker(ids[0]+".md", string(content))
	if err != nil {
		t.Fatal(err)
	}
	if marker.Entry != implAnchorID || !marker.Exclusive {
		t.Fatalf("marker = %+v, want an exclusive marker on %s", marker, implAnchorID)
	}
	return marker
}

func entryOnDisk(t *testing.T, graphDir, id string) bool {
	t.Helper()
	rel, err := model.IDToRelPath(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(graphDir, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// implToSetup drives a fresh instance through the contract to setup,
// logging the anchor read the contract step requires.
func implToSetup(t *testing.T, session *proctest.Session, params map[string]any) *sdd.WorkflowServe {
	t.Helper()
	serve := session.Start(t, "implementation", params)
	proctest.RequireStep(t, serve, "contract")
	session.LogRead(t, "show", []string{implAnchorID}, nil)
	serve = session.Report(t, serve.Instance, map[string]any{
		"contract":    "AC1 remaining, AC2 covered by a partial done; ready to build",
		"widenReport": "searched constraints and prior attempts; nothing beyond the chain",
	})
	proctest.RequireStep(t, serve, "setup")
	return serve
}

// startImplementationAtWork drives a fresh instance through contract and a
// tracked in-place setup to the working junction.
func startImplementationAtWork(t *testing.T, session *proctest.Session) *sdd.WorkflowServe {
	t.Helper()
	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
	serve = session.Answer(t, serve.Instance, "setup", "inPlace",
		map[string]any{"wipDescription": "implement the anchor"}, "in place, small scope")
	proctest.RequireStep(t, serve, "work")
	return serve
}

// enterWorkBranch is the host branching off after setup: the work branch
// carries the marker just written on base, and the agent declares the branch
// as the session binding (20260923-230855-d-cpt-34w).
func enterWorkBranch(t *testing.T, session *proctest.Session, baseDir, workDir, branch string) {
	t.Helper()
	for _, id := range wipMarkerIDs(t, baseDir) {
		content, err := os.ReadFile(filepath.Join(model.WIPDir(baseDir), id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(model.WIPDir(workDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(model.WIPDir(workDir), id+".md"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bindWorkBranch(t, session, branch)
}

func bindWorkBranch(t *testing.T, session *proctest.Session, branch string) {
	t.Helper()
	if err := session.WF.BindBranch(t.Context(), session.World.Identity, branch, false); err != nil {
		t.Fatal(err)
	}
}

// returnToBase is the host back on base: the agent clears the binding, then
// the work branch's checkout disappears.
func returnToBase(t *testing.T, session *proctest.Session, workBranch string) {
	t.Helper()
	if err := session.WF.BindBranch(t.Context(), session.World.Identity, "", true); err != nil {
		t.Fatal(err)
	}
	session.World.DropBranch(t, workBranch)
}

// driveCapture runs an already-started capture child through playback and
// summary verification, returning the written entry's ID.
func driveCapture(t *testing.T, session *proctest.Session, instance string, fields map[string]any) string {
	t.Helper()
	serve := session.Report(t, instance, fields)
	proctest.RequireStep(t, serve, "playback")
	serve = session.Answer(t, instance, "playback", "confirm", nil, "capture it")
	proctest.RequireStep(t, serve, "verifySummary")
	serve = session.Answer(t, instance, "verifySummary", "faithful", map[string]any{"fidelityNote": "matches the body"}, "")
	proctest.RequireStatus(t, serve, "completed")
	entryID, _ := serve.Produced["entryId"].(string)
	if entryID == "" {
		t.Fatalf("capture produced no entryId: %+v", serve.Produced)
	}
	return entryID
}

// captureDone records the closing done as the real dispatched capture
// sub-move: the child inherits the conclude answer's seed, and the done draft
// satisfies the construction boundary by closing the anchor.
func captureDone(t *testing.T, session *proctest.Session, implInstance string) string {
	t.Helper()
	child := startImplChild(t, session, "capture", implInstance)
	proctest.RequireStep(t, child, "assemble")
	if slices.Contains(child.Missing, "widenReport") {
		t.Fatalf("dispatched capture should inherit the seeded widenReport, missing = %v", child.Missing)
	}
	return driveCapture(t, session, child.Instance, map[string]any{
		"body":       "Implemented the anchor directive; delivered in commit abc1234 with all acceptance criteria addressed.",
		"entryKind":  "done",
		"layer":      "tactical",
		"closes":     []any{implAnchorID},
		"topics":     []any{"implementation/engine"},
		"confidence": "high",
	})
}

// resumedInstanceServe re-attaches from a fresh connection — the application's
// real replay path — and returns the instance's re-served position.
func resumedInstanceServe(t *testing.T, world *proctest.World, sessionID sdd.SessionID, connID, instance string) *sdd.WorkflowServe {
	t.Helper()
	_, result := world.Resume(t, sessionID, connID)
	for i := range result.Open {
		if result.Open[i].Instance == instance {
			return &result.Open[i]
		}
	}
	t.Fatalf("resumed session lost instance %s: %+v", instance, result.Open)
	return nil
}

func requireInstructions(t *testing.T, unit, instructions string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(instructions, want) {
			t.Fatalf("%s instructions missing %q:\n%s", unit, want, instructions)
		}
	}
}

// The instruction moments of 20260923-230855-d-cpt-34w: the binding is
// declared after entering the work branch and cleared after returning to base,
// and landing asks for no report.
func assertSetupMoment(t *testing.T, instructions string) {
	t.Helper()
	requireInstructions(t, "setup", instructions,
		"Entering the work branch is host work",
		"so the work branch carries it",
		"declare it as the session branch binding",
		"In-place and quick runs stay where they are and declare nothing",
	)
}

func assertCloseoutMoment(t *testing.T, instructions string) {
	t.Helper()
	requireInstructions(t, "closeout", instructions,
		"Landing is host work and needs no report here",
		"Once the host is back on base, clear the session binding",
		"**evaluate**",
	)
}

func TestImplementation_HappyPathTracked(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "impl-happy")

	serve := session.Start(t, "implementation", map[string]any{"anchor": implAnchorID})
	proctest.RequireStep(t, serve, "contract")
	if !strings.Contains(serve.Instructions, implAnchorID) {
		t.Errorf("contract unit should serve the anchor's chains, got %q", serve.Instructions)
	}
	instance := serve.Instance

	session.LogRead(t, "show", []string{implAnchorID}, nil)
	serve = session.Report(t, instance, map[string]any{
		"contract":    "AC1 remaining, AC2 covered by a partial done; ready to build",
		"widenReport": "searched constraints and prior attempts; nothing beyond the chain",
	})
	proctest.RequireStep(t, serve, "setup")
	assertSetupMoment(t, serve.Instructions)
	serve = session.Answer(t, instance, "setup", "inPlace",
		map[string]any{"wipDescription": "implement the anchor"}, "in place, small scope")
	proctest.RequireStep(t, serve, "work")
	marker := requireSingleMarker(t, world.GraphDir)
	if marker.Content != "implement the anchor" {
		t.Fatalf("marker content = %q, want the wipDescription", marker.Content)
	}

	// One working-loop cycle: continue self-loops with the running notes.
	serve = session.Answer(t, instance, "work", "continue",
		map[string]any{"progressNotes": "slice 1 committed abc123; next: tests"}, "keep going")
	proctest.RequireStep(t, serve, "work")

	serve = session.Answer(t, instance, "work", "conclude", nil, "contract met")
	proctest.RequireStep(t, serve, "record")

	// The marker goes right after the done is recorded; no landing report.
	doneID := captureDone(t, session, instance)
	serve = session.Report(t, instance, map[string]any{"doneEntry": doneID})
	proctest.RequireStep(t, serve, "closeout")
	requireNoMarkers(t, world.GraphDir)
	assertCloseoutMoment(t, serve.Instructions)

	serve = session.Answer(t, instance, "closeout", "finish", nil, "done for today")
	proctest.RequireStatus(t, serve, "completed")
}

// TestImplementation_MarkerFollowsTheCurrentBranchInEveryMode drives every
// tracked mode: setup writes the marker on the session's current branch, the
// work branch the host branches off carries it, and the engine removes it
// there right after the done — base keeps showing the work as taken until the
// merge (20260923-230855-d-cpt-34w).
func TestImplementation_MarkerFollowsTheCurrentBranchInEveryMode(t *testing.T) {
	for _, mode := range []string{"inPlace", "branch", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			featureDir := t.TempDir()
			proctest.WriteEntry(t, featureDir, implAnchorEntry())
			proctest.WriteEntry(t, featureDir, implDoneEntry())
			world := proctest.NewWorld(t,
				proctest.WithEntries(implAnchorEntry(), implDoneEntry()),
				proctest.WithBranchDir("feature", featureDir),
			)
			session := world.Open(t, "impl-routes-"+mode)

			serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
			instance := serve.Instance
			serve = session.Answer(t, instance, "setup", mode, map[string]any{"wipDescription": "route targets"}, mode)
			proctest.RequireStep(t, serve, "work")
			if len(serve.Missing) != 0 {
				t.Fatalf("the working junction demands a report: missing=%v", serve.Missing)
			}
			requireSingleMarker(t, world.GraphDir)
			requireNoMarkers(t, featureDir)
			if mode != "inPlace" {
				enterWorkBranch(t, session, world.GraphDir, featureDir, "feature")
			}

			serve = session.Answer(t, instance, "work", "conclude", nil, "mode routing verified")
			proctest.RequireStep(t, serve, "record")
			serve = session.Report(t, instance, map[string]any{"doneEntry": implDoneID})
			proctest.RequireStep(t, serve, "closeout")
			if mode == "inPlace" {
				requireNoMarkers(t, world.GraphDir)
				return
			}
			requireNoMarkers(t, featureDir)
			requireSingleMarker(t, world.GraphDir)
		})
	}
}

// TestImplementation_AbandonAfterSetupRemovesMarker is the abandon path: a
// tracked run dropped at the working junction removes its marker on the
// session's current branch and ends without a done — on base once the host
// returned there and cleared the binding; a quick run ends the same way with
// nothing to remove.
func TestImplementation_AbandonAfterSetupRemovesMarker(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
		session := world.Open(t, "impl-abandon")
		serve := startImplementationAtWork(t, session)
		instance := serve.Instance
		requireInstructions(t, "work", serve.Instructions, "**abandon**", "return it to base and clear the session binding first")
		requireSingleMarker(t, world.GraphDir)

		serve = session.Answer(t, instance, "work", "abandon", nil, "drop this run")
		proctest.RequireStatus(t, serve, "abandoned")
		requireNoMarkers(t, world.GraphDir)
	})
	t.Run("from a work branch", func(t *testing.T) {
		featureDir := t.TempDir()
		proctest.WriteEntry(t, featureDir, implAnchorEntry())
		world := proctest.NewWorld(t,
			proctest.WithEntries(implAnchorEntry()),
			proctest.WithBranchDir("feature", featureDir),
		)
		session := world.Open(t, "impl-abandon-branch")
		serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
		instance := serve.Instance
		serve = session.Answer(t, instance, "setup", "branch", map[string]any{"wipDescription": "dropped later"}, "on a branch")
		proctest.RequireStep(t, serve, "work")
		enterWorkBranch(t, session, world.GraphDir, featureDir, "feature")

		returnToBase(t, session, "feature")
		serve = session.Answer(t, instance, "work", "abandon", nil, "drop this run")
		proctest.RequireStatus(t, serve, "abandoned")
		requireNoMarkers(t, world.GraphDir)
	})
	t.Run("quick", func(t *testing.T) {
		world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
		session := world.Open(t, "impl-abandon-quick")
		serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
		instance := serve.Instance
		serve = session.Answer(t, instance, "setup", "quick", nil, "too small to track")
		proctest.RequireStep(t, serve, "work")

		serve = session.Answer(t, instance, "work", "abandon", nil, "drop this run")
		proctest.RequireStatus(t, serve, "abandoned")
		requireNoMarkers(t, world.GraphDir)
	})
}

// TestImplementation_StaleBindingCanBeCleared: a session bound to a branch
// whose checkout is gone must still replay, so the binding can be cleared and
// the session resumed.
func TestImplementation_StaleBindingCanBeCleared(t *testing.T) {
	featureDir := t.TempDir()
	proctest.WriteEntry(t, featureDir, implAnchorEntry())
	world := proctest.NewWorld(t,
		proctest.WithEntries(implAnchorEntry()),
		proctest.WithBranchDir("feature", featureDir),
	)
	session := world.Open(t, "impl-stale")
	serve := startImplementationAtWork(t, session)
	instance := serve.Instance
	bindWorkBranch(t, session, "feature")
	world.DropBranch(t, "feature")

	if _, _, err := world.App.ResumeWorkflow(t.Context(), world.Identity, sdd.WorkflowResumeRequest{SessionID: session.ID, ClientName: "impl-stale-resume"}); err == nil {
		t.Fatal("resuming a session bound to a branch without a checkout succeeded")
	} else if !strings.Contains(err.Error(), `session is bound to branch "feature"`) || !strings.Contains(err.Error(), "clear it") {
		t.Fatalf("stale binding error = %v, want the binding named with the clear advice", err)
	}

	refreshed, err := world.App.RefreshWorkflow(t.Context(), world.Identity, session.ID)
	if err != nil {
		t.Fatalf("loading the session to clear its binding: %v", err)
	}
	if err := refreshed.BindBranch(t.Context(), world.Identity, "", true); err != nil {
		t.Fatalf("clearing the stale binding: %v", err)
	}
	resumed := resumedInstanceServe(t, world, session.ID, "impl-stale-resumed", instance)
	proctest.RequireStep(t, resumed, "work")
}

func TestImplementation_QuickSkipsMarker(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry(), implDoneEntry()))
	session := world.Open(t, "impl-quick")

	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
	instance := serve.Instance
	serve = session.Answer(t, instance, "setup", "quick", nil, "too small to track")
	proctest.RequireStep(t, serve, "work")
	requireNoMarkers(t, world.GraphDir)

	serve = session.Answer(t, instance, "work", "conclude", nil, "fixed")
	proctest.RequireStep(t, serve, "record")
	// No marker was created, so record must bypass the removal — a route
	// through wipDone would fail loudly on the unset wipMarker.
	serve = session.Report(t, instance, map[string]any{"doneEntry": implDoneID})
	proctest.RequireStep(t, serve, "closeout")
	assertCloseoutMoment(t, serve.Instructions)
	requireNoMarkers(t, world.GraphDir)
}

func TestImplementation_HoldLoopsBackToSetup(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "impl-hold")

	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
	instance := serve.Instance

	// Hold stashes the capture seed and re-serves setup: the missing decision
	// is captured as a sub-move, then the user picks a mode.
	serve = session.Answer(t, instance, "setup", "hold", nil, "decide the format first")
	proctest.RequireStep(t, serve, "setup")
	requireNoMarkers(t, world.GraphDir)

	serve = session.Answer(t, instance, "setup", "inPlace",
		map[string]any{"wipDescription": "implement with the decided format"}, "decided, go")
	proctest.RequireStep(t, serve, "work")
}

// TestImplementation_HoldCaptureFollowsSessionBinding is the behavioral half
// of the dispatch-declaration check for hold: the dispatched capture inherits
// widenReport and no branch, so the captured decision lands where the session
// binding points, not on a branch the run names.
func TestImplementation_HoldCaptureFollowsSessionBinding(t *testing.T) {
	featureDir := t.TempDir()
	proctest.WriteEntry(t, featureDir, implAnchorEntry())
	world := proctest.NewWorld(t,
		proctest.WithEntries(implAnchorEntry()),
		proctest.WithBranchDir("feature", featureDir),
	)
	session := world.Open(t, "impl-hold-seed")
	bindWorkBranch(t, session, "feature")

	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
	instance := serve.Instance
	serve = session.Answer(t, instance, "setup", "hold", nil, "decide the format first")
	proctest.RequireStep(t, serve, "setup")

	child := startImplChild(t, session, "capture", instance)
	proctest.RequireStep(t, child, "assemble")
	if slices.Contains(child.Missing, "widenReport") {
		t.Fatalf("hold dispatch should seed widenReport, missing = %v", child.Missing)
	}
	entryID := driveCapture(t, session, child.Instance, map[string]any{
		"body":       "The output format is JSON lines, one record per entry.",
		"entryKind":  "directive",
		"layer":      "tactical",
		"intent":     "pending",
		"refs":       []any{map[string]any{"id": implAnchorID, "kind": "addresses"}},
		"confidence": "medium",
	})
	if !entryOnDisk(t, featureDir, entryID) {
		t.Fatalf("hold capture %s should land on the session-bound store", entryID)
	}
	if entryOnDisk(t, world.GraphDir, entryID) {
		t.Fatalf("hold capture %s leaked onto the base store", entryID)
	}
}

func TestImplementation_BlockedLoopsBackToWork(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "impl-blocked")
	serve := startImplementationAtWork(t, session)
	instance := serve.Instance

	serve = session.Answer(t, instance, "work", "blocked",
		map[string]any{"roadblock": "no decision defines staleness for markers"}, "stopping to dialogue")
	proctest.RequireStep(t, serve, "work")

	// The blocked answer seeds any capture the dialogue produces.
	child := startImplChild(t, session, "capture", instance)
	proctest.RequireStep(t, child, "assemble")
	if slices.Contains(child.Missing, "widenReport") {
		t.Fatalf("blocked dispatch should seed widenReport, missing = %v", child.Missing)
	}

	// The dialogue resolved it; the run continues in place.
	serve = session.Answer(t, instance, "work", "continue",
		map[string]any{"progressNotes": "staleness decided in dialogue; resuming slice 2"}, "resolved")
	proctest.RequireStep(t, serve, "work")
}

func TestImplementation_DoneEntryMustResolve(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "impl-unresolved-done")
	serve := startImplementationAtWork(t, session)
	instance := serve.Instance

	serve = session.Answer(t, instance, "work", "conclude", nil, "wrapping up")
	proctest.RequireStep(t, serve, "record")
	serve = session.Report(t, instance, map[string]any{"doneEntry": implMissingID})
	proctest.RequireStep(t, serve, "record")
	if joined := strings.Join(serve.Diagnostics, "\n"); !strings.Contains(joined, "doneEntry does not resolve") {
		t.Fatalf("diagnostics = %v, want the doneEntryResolves failure", serve.Diagnostics)
	}
}

// TestImplementation_NothingReadsTheWorkBranchAfterItIsGone answers
// 20260902-160151-s-tac-mtv: the done and the marker removal land on the bound
// work branch; once the host is back on base and the work branch's checkout is
// gone, the run still finishes and replays on base.
func TestImplementation_NothingReadsTheWorkBranchAfterItIsGone(t *testing.T) {
	featureDir := t.TempDir()
	proctest.WriteEntry(t, featureDir, implAnchorEntry())
	world := proctest.NewWorld(t,
		proctest.WithEntries(implAnchorEntry()),
		proctest.WithBranchDir("feature", featureDir),
	)
	session := world.Open(t, "impl-workbranch")

	serve := implToSetup(t, session, map[string]any{"anchor": implAnchorID})
	instance := serve.Instance
	serve = session.Answer(t, instance, "setup", "worktree",
		map[string]any{"wipDescription": "target-aware reads"}, "use a worktree")
	proctest.RequireStep(t, serve, "work")
	enterWorkBranch(t, session, world.GraphDir, featureDir, "feature")
	serve = session.Answer(t, instance, "work", "conclude", nil, "contract met")
	proctest.RequireStep(t, serve, "record")

	// The dispatched capture follows the session binding, so the done exists
	// only on the feature store — record's resolution reads through the
	// binding to find it.
	doneID := captureDone(t, session, instance)
	if !entryOnDisk(t, featureDir, doneID) {
		t.Fatalf("done capture %s should land on the bound work branch store", doneID)
	}
	if entryOnDisk(t, world.GraphDir, doneID) {
		t.Fatalf("done capture %s leaked onto the base store", doneID)
	}
	serve = session.Report(t, instance, map[string]any{"doneEntry": doneID})
	proctest.RequireStep(t, serve, "closeout")
	requireNoMarkers(t, featureDir)

	returnToBase(t, session, "feature")
	replayed := resumedInstanceServe(t, world, session.ID, "impl-workbranch-replay", instance)
	proctest.RequireStep(t, replayed, "closeout")
	resumed, _ := world.Resume(t, session.ID, "impl-workbranch-finish")
	serve = resumed.Answer(t, instance, "closeout", "finish", nil, "merged")
	proctest.RequireStatus(t, serve, "completed")
}

// TestImplementation_DispatchSeedsChildren is the behavioral port of the old
// spec-level dispatch-declaration test: conclude's capture child inherits the
// grounding (asserted inside captureDone), and closeout's evaluate child is
// anchored on the fresh done signal.
func TestImplementation_DispatchSeedsChildren(t *testing.T) {
	world := proctest.NewWorld(t, proctest.WithEntries(implAnchorEntry()))
	session := world.Open(t, "impl-dispatch")
	serve := startImplementationAtWork(t, session)
	instance := serve.Instance

	serve = session.Answer(t, instance, "work", "conclude", nil, "contract met")
	proctest.RequireStep(t, serve, "record")
	doneID := captureDone(t, session, instance)
	serve = session.Report(t, instance, map[string]any{"doneEntry": doneID})
	proctest.RequireStep(t, serve, "closeout")
	serve = session.Answer(t, instance, "closeout", "evaluate", nil, "evaluate it")
	proctest.RequireStatus(t, serve, "completed")

	child := startImplChild(t, session, "evaluate", instance)
	proctest.RequireStep(t, child, "scope")
	if slices.Contains(child.Missing, "widenReport") {
		t.Fatalf("evaluate dispatch should seed widenReport, missing = %v", child.Missing)
	}
	if !strings.Contains(child.Instructions, doneID) {
		t.Fatalf("evaluate scope should serve the seeded done anchor's chains:\n%s", child.Instructions)
	}
}
