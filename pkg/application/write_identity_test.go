package application_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/networkteam/sdd/internal/model"
	sdd "github.com/networkteam/sdd/pkg/application"
	pkgllm "github.com/networkteam/sdd/pkg/llm"
	localadapter "github.com/networkteam/sdd/pkg/local"
)

// End-to-end write-path coverage for the slice-1 identity kinds: an actor and a
// role flow through CreateEntry with their kind-specific fields carried onto the
// entry, persist, and pass model validation; a role missing its required actor
// field surfaces the actionable *ValidationError (§7, s-prc-g0j) instead of a
// silent write.

type writeAppOptions struct {
	Runner       pkgllm.Runner
	Finalizers   []sdd.MutationFinalizer
	SourceConfig *sdd.ProjectConfig
	Dependency   *sdd.ProjectRuntime
}

func newIdentityWriteApp(t *testing.T, options ...writeAppOptions) (*sdd.Application, sdd.RequestIdentity, sdd.SessionBinding, string) {
	t.Helper()
	dir := t.TempDir()
	baseGraph, err := localadapter.NewFilesystemGraphStore(localadapter.FilesystemGraphStoreOptions{Project: "example", GraphDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := localadapter.NewFilesystemSessionStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := localadapter.NewFilesystemStagedBlobStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := pkgllm.Runner(pkgllm.RunnerFunc(func(_ context.Context, request pkgllm.Request) (pkgllm.Result, error) {
		identity := pkgllm.Identity{Provider: "test", Model: "test"}
		if request.Purpose == pkgllm.PurposePreflight {
			return pkgllm.Result{Text: `{"findings":[]}`, Identity: identity}, nil
		}
		return pkgllm.Result{Text: "An identity captured for the project record.", Identity: identity}, nil
	}))
	var finalizers []sdd.MutationFinalizer
	var graph sdd.GraphStore = baseGraph
	var dependency *sdd.ProjectRuntime
	for _, option := range options {
		if option.Runner != nil {
			runner = option.Runner
		}
		finalizers = append(finalizers, option.Finalizers...)
		if option.SourceConfig != nil {
			reader := acquiredReadStore{acquire: func(ctx context.Context, query sdd.SnapshotReadQuery) (*sdd.AcquiredSnapshot, error) {
				source, err := baseGraph.AcquireSnapshot(ctx, query)
				if err != nil {
					return nil, err
				}
				source.Config = option.SourceConfig
				return source, nil
			}}
			graph = struct {
				sdd.GraphStore
				sdd.SnapshotReader
				sdd.EntryPublicationStore
			}{baseGraph, reader, baseGraph}
		}
		if option.Dependency != nil {
			dependency = option.Dependency
		}
	}
	runtime, err := sdd.NewProjectRuntime(sdd.ProjectRuntimeOptions{
		Project: sdd.ProjectRef{ID: "example"}, DefaultBranch: "main", Graph: graph,
		LLM: runner, Finalizers: finalizers,
	})
	if err != nil {
		t.Fatal(err)
	}
	var access sdd.AccessResolver = &runtimeAccessResolver{runtime: runtime}
	if dependency != nil {
		access = &multiAccessResolver{base: runtime, dependency: dependency}
	}
	application, err := sdd.NewApplication(sdd.ApplicationOptions{Access: access, Sessions: sessions, StagedBlobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	identity := sdd.RequestIdentity{Subject: "christopher"}
	return application, identity, openBinding(t, sessions, identity.Subject, "identity-write"), dir
}

func loadEntryByID(t *testing.T, dir, id string) *model.Entry {
	t.Helper()
	rel, err := model.IDToRelPath(id)
	if err != nil {
		t.Fatalf("IDToRelPath(%s): %v", id, err)
	}
	path := filepath.Join(dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted entry %s at %s: %v", id, path, err)
	}
	// ParseEntry derives type/layer/time from the filename, so hand it the full
	// ID (the on-disk basename is DD-HHmmss-… under YYYY/MM).
	e, err := model.ParseEntry(id+".md", string(data))
	if err != nil {
		t.Fatalf("parsing persisted entry %s: %v", id, err)
	}
	return e
}

var captureSequence atomic.Uint64

func captureEntry(t *testing.T, app *sdd.Application, identity sdd.RequestIdentity, project sdd.ProjectID, binding sdd.SessionBinding, draft sdd.EntryDraft) (sdd.CreateEntryResult, error) {
	t.Helper()
	preflight, err := app.PreflightEntry(t.Context(), identity, project, binding, draft)
	if err != nil {
		return sdd.CreateEntryResult{}, err
	}
	for _, finding := range preflight.Findings {
		if finding.Severity == "high" {
			return sdd.CreateEntryResult{Findings: preflight.Findings}, nil
		}
	}
	draft.Target = preflight.Target
	draft = recordedCaptureDraft(binding, draft)
	created, err := app.CreateEntry(t.Context(), identity, project, binding, draft)
	created.Findings = preflight.Findings
	return created, err
}

func recordedCaptureDraft(binding sdd.SessionBinding, draft sdd.EntryDraft) sdd.EntryDraft {
	sequence := captureSequence.Add(1)
	entryType := model.TypeSignal
	if model.IsValidKindForType(model.TypeDecision, model.Kind(draft.Kind)) {
		entryType = model.TypeDecision
	}
	draft.EntryID = model.GenerateIDAt(entryType, model.Layer(draft.Layer), fmt.Sprintf("api%d", sequence), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	draft.Publication = sdd.PublicationKey{Session: binding.SessionID, Sequence: sequence, Discriminator: "new-entry"}
	return draft
}

func TestCreateEntry_ActorPersistsWithCanonicalAndAliases(t *testing.T) {
	app, identity, binding, dir := newIdentityWriteApp(t)
	created, err := captureEntry(t, app, identity, "example", binding, sdd.EntryDraft{
		Kind: "actor", Layer: "process", Confidence: "high",
		Body:      "Christopher is CEO of networkteam with a full-stack background.",
		Canonical: "Christopher", Aliases: []string{"Chris", "CH"},
	})
	if err != nil || created.EntryID == "" {
		t.Fatalf("actor CreateEntry = %+v, err %v", created, err)
	}
	e := loadEntryByID(t, dir, created.EntryID)
	if !e.IsActor() || e.Canonical != "Christopher" {
		t.Fatalf("persisted entry is not an actor with canonical Christopher: kind=%s canonical=%q", e.Kind, e.Canonical)
	}
	if len(e.Aliases) != 2 || e.Aliases[0] != "Chris" || e.Aliases[1] != "CH" {
		t.Errorf("aliases = %v, want [Chris CH]", e.Aliases)
	}
}

func TestCreateEntry_RolePassesValidationWithBoundActor(t *testing.T) {
	app, identity, binding, _ := newIdentityWriteApp(t)
	actor, err := captureEntry(t, app, identity, "example", binding, sdd.EntryDraft{Kind: "actor", Layer: "process", Confidence: "high", Canonical: "Christopher", Body: "Christopher participates in this project."})
	if err != nil {
		t.Fatal(err)
	}
	role, err := captureEntry(t, app, identity, "example", binding, sdd.EntryDraft{
		Kind: "role", Layer: "process", Confidence: "high",
		Body: "Christopher holds the strategic and conceptual calls on this project.", Actor: "Christopher",
		Refs: []sdd.EntryRef{{ID: actor.EntryID, Kind: "grounded-in"}},
	})
	if err != nil || role.EntryID == "" {
		t.Fatalf("role CreateEntry with a bound actor should pass validation and write, got %+v, err %v", role, err)
	}
}

func TestCreateEntry_RoleMissingActorReturnsActionableValidationError(t *testing.T) {
	app, identity, binding, _ := newIdentityWriteApp(t)
	_, err := captureEntry(t, app, identity, "example", binding, sdd.EntryDraft{
		Kind: "role", Layer: "process", Confidence: "high",
		Body: "A role with no bound actor.",
	})
	var verr *sdd.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	named := false
	for _, w := range verr.Warnings {
		if w.Field == "actor" {
			named = true
		}
	}
	if !named {
		t.Fatalf("ValidationError should name the missing actor field, got %+v", verr.Warnings)
	}
}
