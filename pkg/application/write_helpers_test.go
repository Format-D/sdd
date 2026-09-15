package application_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/networkteam/sdd/internal/model"
	sdd "github.com/networkteam/sdd/pkg/application"
)

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

func preflightAndCreateEntry(t *testing.T, app *sdd.Application, identity sdd.RequestIdentity, binding sdd.SessionBinding, draft sdd.EntryDraft) (sdd.CreateEntryResult, error) {
	t.Helper()
	preflight, err := app.PreflightEntry(t.Context(), identity, "example", binding, draft)
	if err != nil {
		return sdd.CreateEntryResult{}, err
	}
	for _, finding := range preflight.Findings {
		if finding.Severity == "high" {
			return sdd.CreateEntryResult{Findings: preflight.Findings}, nil
		}
	}
	draft.Target = preflight.Target
	created, err := app.CreateEntry(t.Context(), identity, "example", binding, draft)
	created.Findings = preflight.Findings
	return created, err
}

func identifiedCaptureDraft(binding sdd.SessionBinding, sequence uint64, draft sdd.EntryDraft) sdd.EntryDraft {
	entryType := model.TypeSignal
	if model.IsValidKindForType(model.TypeDecision, model.Kind(draft.Kind)) {
		entryType = model.TypeDecision
	}
	draft.EntryID = model.GenerateIDAt(entryType, model.Layer(draft.Layer), fmt.Sprintf("api%d", sequence), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	draft.Publication = sdd.PublicationKey{Session: binding.SessionID, Sequence: sequence, Discriminator: "new-entry"}
	return draft
}

func validationErrorMentions(t *testing.T, err error, field string) {
	t.Helper()
	var validationErr *sdd.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	for _, w := range validationErr.Warnings {
		if w.Field == field {
			return
		}
	}
	t.Fatalf("no finding on field %q in %v", field, validationErr.Warnings)
}

func (f *writeFixture) createFocusTarget(t *testing.T) string {
	t.Helper()
	draft := f.identifiedDraft(sdd.EntryDraft{
		Kind: "directive", Layer: "tactical", Intent: "pending", Confidence: "high",
		Body: "A directive the focus advances.",
	})
	target, err := f.app.CreateEntry(t.Context(), f.identity, "example", f.binding, draft)
	if err != nil || target.EntryID == "" {
		t.Fatalf("target CreateEntry = %+v, %v", target, err)
	}
	return target.EntryID
}
