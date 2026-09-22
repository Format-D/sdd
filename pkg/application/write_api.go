package application

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/pkg/application/types"
)

type Finding struct {
	Severity    string
	Category    string
	Observation string
}

type EntryRef struct {
	ID   string
	Kind string
	Desc string
}

type FactIndex struct {
	Title string `json:"title"`
	Topic string `json:"topic"`
}

type EntryDraft struct {
	EntryID           string
	Publication       PublicationKey
	Target            MutationTarget
	Kind              string
	Layer             string
	Intent            string
	Body              string
	Refs              []EntryRef
	Closes            []string
	Supersedes        []string
	Participants      []string
	Confidence        string
	Topics            []string
	Index             *FactIndex
	AttachmentHandles []string
	Attachments       []StagedAttachment
	// Canonical and Aliases carry a kind: actor signal's identity; Actor carries
	// a kind: role decision's bound actor canonical; Class carries a
	// kind: procedure decision's execution role. Mirrors the CLI-side
	// NewEntryCmd fields — a value on the wrong kind is a blocking finding at
	// the construction boundary.
	Canonical string
	Aliases   []string
	Actor     string
	Class     string
	// ProcedureSpec carries a kind: procedure decision's workflow declaration
	// as one structured document — {params?, state?, steps, framing?} —
	// converted strictly at draft-to-entry assembly. Interpretation stays
	// with the engine.
	ProcedureSpec map[string]any
	// FocusActors, FocusWhen, and Involvement carry a kind: focus decision's
	// advances list and its focus-level defaults. Mirrors the CLI-side
	// NewEntryCmd fields — ignored on other kinds, written onto the entry so
	// the model-layer validator sees the required involvement frontmatter.
	FocusActors   []string
	FocusWhen     *types.FocusWhen
	Involvement   []types.Involvement
	SkipPreflight bool
}

type StagedAttachment struct {
	Filename string
	BlobID   string
}

type PreflightEntryResult struct {
	Target   MutationTarget
	Findings []Finding
}

// ValidationError reports that model.ValidateEntry rejected a draft at the
// write gate. It carries the structural warnings so the workflow gate can
// re-serve them as actionable findings — naming the violated rule and the
// field — and route the instance back to a step that can fix it, rather than
// wedging behind an opaque hard error (closes half of s-prc-g0j).
type ValidationError struct {
	Warnings []types.Warning
}

func (e *ValidationError) Error() string {
	if len(e.Warnings) == 0 {
		return "validation failed"
	}
	parts := make([]string, 0, len(e.Warnings))
	for _, w := range e.Warnings {
		parts = append(parts, w.Message)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

type CreateEntryResult struct {
	Project  ProjectRef
	Binding  SessionBinding
	EntryID  string
	Summary  string
	Findings []Finding
}

// CurrentSnapshot resolves current read access and returns the opaque
// canonical snapshot for protocol adapters that host SDD's engine.
func (a *Application) CurrentSnapshot(ctx context.Context, identity RequestIdentity, project ProjectID) (*Snapshot, error) {
	_, runtime, err := a.resolve(ctx, identity, project, AccessRead)
	if err != nil {
		return nil, err
	}
	snapshot, _, err := readMaterializedSnapshot(ctx, runtime, "")
	return snapshot, err
}

// StageBlob resolves current read access before placing immutable bytes in
// session-scoped scratch. Canonical write access is checked later at the
// mutation gate, so read-only principals can still conduct dialogue.
func (a *Application) StageBlob(ctx context.Context, identity RequestIdentity, project ProjectID, ref SessionRef, filename string, content []byte) (StagedBlob, error) {
	principal, _, err := a.resolve(ctx, identity, project, AccessRead)
	if err != nil {
		return StagedBlob{}, err
	}
	if ref.Subject != principal.Subject {
		return StagedBlob{}, &ApplicationError{Code: ErrorSessionOwnership, Message: "staged blobs belong to another principal"}
	}
	return a.blobs.Stage(ctx, ref, filename, bytes.NewReader(content))
}

// OpenStagedBlob resolves read access and session ownership, then streams a
// staged blob's bytes — the read-side counterpart of StageBlob.
func (a *Application) OpenStagedBlob(ctx context.Context, identity RequestIdentity, project ProjectID, ref SessionRef, blobID string) (io.ReadCloser, error) {
	principal, _, err := a.resolve(ctx, identity, project, AccessRead)
	if err != nil {
		return nil, err
	}
	if ref.Subject != principal.Subject {
		return nil, &ApplicationError{Code: ErrorSessionOwnership, Message: "staged blobs belong to another principal"}
	}
	return a.blobs.Open(ctx, ref, blobID)
}

// resolveMutationTarget completes a target against the runtime it is written
// through: an empty branch means the runtime's configured default, and a named
// project must be the runtime's own.
func resolveMutationTarget(runtime *ProjectRuntime, requested MutationTarget) (MutationTarget, error) {
	if requested.Project == "" {
		requested.Project = runtime.options.Project.ID
	}
	if requested.Branch == "" {
		if requested.Project != runtime.options.Project.ID {
			return MutationTarget{}, &ApplicationError{Code: ErrorWriteDenied, Message: "mutation target project must equal the session project"}
		}
		return runtime.defaultMutationTarget()
	}
	if err := requested.Validate(runtime.options.Project.ID); err != nil {
		return MutationTarget{}, err
	}
	return requested, nil
}

// draftLayer expands an abbreviated layer to its canonical form.
func draftLayer(layer string) model.Layer {
	if expanded, ok := model.LayerFromAbbrev[layer]; ok {
		return expanded
	}
	return model.Layer(layer)
}

// entryFromDraft materializes a draft's fields as a model.Entry — the one
// draft-to-entry assembly, shared by the write gate and the assemble-gate
// predicate (draftValidates). Shape problems — missing or unknown kind,
// malformed topic or index — come back as findings rather than errors, so
// both callers serve them as actionable diagnostics.
func entryFromDraft(draft EntryDraft, id string, now time.Time) (*model.Entry, []model.Finding) {
	kind := model.Kind(draft.Kind)
	entryType, err := entryTypeForKind(kind)
	if err != nil {
		return nil, []model.Finding{{Field: "kind", Value: draft.Kind, Message: err.Error()}}
	}
	var findings []model.Finding
	topics := make([]model.TopicPath, 0, len(draft.Topics))
	for _, label := range draft.Topics {
		topic, err := model.ParseTopicPath(label)
		if err != nil {
			findings = append(findings, model.Finding{Field: "topics", Value: label, Message: fmt.Sprintf("topic %q: %v", label, err)})
			continue
		}
		topics = append(topics, topic)
	}
	var index *model.FactIndex
	if draft.Index != nil {
		index, err = model.NewFactIndex(draft.Index.Title, draft.Index.Topic)
		if err != nil {
			findings = append(findings, model.Finding{Field: "index", Message: err.Error()})
		}
	}
	entry := &model.Entry{
		ID: id, Type: entryType, Kind: kind, Layer: draftLayer(draft.Layer), Intent: model.Intent(draft.Intent),
		Content: draft.Body, Participants: append([]string(nil), draft.Participants...),
		Confidence: draft.Confidence, Topics: topics, Index: index, Time: now,
		Canonical: draft.Canonical, Aliases: append([]string(nil), draft.Aliases...), Actor: draft.Actor,
		Class:       model.ProcedureClass(draft.Class),
		FocusActors: append([]string(nil), draft.FocusActors...), FocusWhen: draft.FocusWhen,
		Involvement: append([]types.Involvement(nil), draft.Involvement...),
	}
	if len(draft.ProcedureSpec) > 0 {
		if kind != model.KindProcedure {
			findings = append(findings, model.Finding{Field: "procedureSpec", Message: "a workflow declaration is only meaningful on a kind: procedure decision"})
		} else if spec, err := model.ProcedureSpecFromDocument(draft.ProcedureSpec); err != nil {
			findings = append(findings, model.Finding{Field: "procedureSpec", Message: err.Error()})
		} else {
			entry.ProcedureSpec = spec
		}
	}
	for _, ref := range draft.Refs {
		entry.Refs = append(entry.Refs, model.Ref{ID: ref.ID, Kind: model.RefKind(ref.Kind), Desc: ref.Desc})
	}
	entry.Closes = append([]string(nil), draft.Closes...)
	entry.Supersedes = append([]string(nil), draft.Supersedes...)
	return entry, findings
}

func entryTypeForKind(kind model.Kind) (model.EntryType, error) {
	// An empty kind is valid for both types at the model layer (defaults are
	// a construction concern), so it must be rejected here — deriving the
	// type from it would silently mint a kindless signal.
	if kind == "" {
		return "", fmt.Errorf("entry kind is required")
	}
	switch {
	case model.IsValidKindForType(model.TypeSignal, kind):
		return model.TypeSignal, nil
	case model.IsValidKindForType(model.TypeDecision, kind):
		return model.TypeDecision, nil
	default:
		return "", fmt.Errorf("unknown entry kind %q", kind)
	}
}
