package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/networkteam/slogutils"

	"github.com/networkteam/sdd/internal/engine"
	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/internal/query"
)

var captureDraftFields = []string{"body", "entryKind", "layer", "refs", "topics", "index", "confidence", "intent", "attachments", "participants", "supersedes", "closes", "canonical", "class", "procedureSpec", "aliases", "roleActor", "involvement", "focusActors", "focusWhen", "captureBranch"}

func captureAttachments(draft *EntryDraft, staged map[string]string) error {
	for _, filename := range draft.AttachmentHandles {
		blob, ok := staged[filename]
		if !ok {
			return fmt.Errorf("attachment %q has not been staged in this session", filename)
		}
		draft.Attachments = append(draft.Attachments, StagedAttachment{Filename: filename, BlobID: blob})
	}
	return nil
}

func (w *WorkflowSession) runWorkflowPreflight(ctx *engine.Context) error {
	target, fromBinding := w.effectiveTargetFor(w.instanceProject(ctx.Instance), ctx.Store)
	if err := w.authorizeTarget(target.Project, AccessWrite); err != nil {
		return err
	}
	draft := w.draftFromStore(ctx.Store)
	draft.Target = target
	if err := captureAttachments(&draft, w.staged); err != nil {
		return err
	}
	result, err := w.app.PreflightEntry(w.ctx, w.identity, target.Project, w.binding, draft)
	err = w.withSessionBindingTargetError(err, fromBinding)
	if target.Branch == "" {
		err = withBaseTargetError("", err)
	}
	var validation *ValidationError
	if errors.As(err, &validation) {
		for _, warning := range validation.Warnings {
			result.Findings = append(result.Findings, Finding{Severity: string(query.SeverityHigh), Category: "validation", Observation: warning.Message})
		}
	} else if err != nil {
		return err
	}
	findings := make([]query.Finding, 0, len(result.Findings))
	for _, finding := range result.Findings {
		findings = append(findings, query.Finding{Severity: query.Severity(finding.Severity), Category: finding.Category, Observation: finding.Observation})
	}
	if err := ctx.Store.WriteEngine("findings", findings); err != nil {
		return err
	}
	if err := ctx.Store.WriteEngine("preflightOverride", nil); err != nil {
		return err
	}
	return ctx.Store.WriteEngine("resolvedCaptureBranch", result.Target.Branch)
}

func (w *WorkflowSession) prepareWorkflowNewEntry(ctx *engine.Context) (map[string]string, error) {
	if err := w.verifyCapturePreflight(ctx); err != nil {
		return nil, err
	}
	branch, _ := workflowStoreString(ctx.Store, "resolvedCaptureBranch")
	if branch == "" {
		return nil, fmt.Errorf("capture has no preflight-resolved branch")
	}
	kind, _ := workflowStoreString(ctx.Store, "entryKind")
	layer, _ := workflowStoreString(ctx.Store, "layer")
	entryType, err := entryTypeForKind(model.Kind(kind))
	if err != nil {
		return nil, err
	}
	suffix, err := w.app.entrySuffix(3)
	if err != nil {
		return nil, err
	}
	id := model.GenerateIDAt(entryType, draftLayer(layer), suffix, w.app.now())
	project := w.instanceProject(ctx.Instance)
	_, source := w.effectiveTargetSource(project, ctx.Store)
	if source == "" {
		source = targetSourceBase
	}
	return map[string]string{"entryId": id, "project": string(project), "branch": branch, "source": source}, nil
}

// newEntryPublicationKey is the capture's storage identity: this session, the
// intent's position and the command as discriminator (d-tac-n47).
func (w *WorkflowSession) newEntryPublicationKey(intent *engine.MutationIntent) PublicationKey {
	return PublicationKey{Session: w.ID(), Sequence: intent.Ref, Discriminator: intent.Command}
}

// reportWorkflowNewEntryEffects reports the capture's entry as published,
// absent, or unknown when its store cannot answer (d-tac-7mh).
func (w *WorkflowSession) reportWorkflowNewEntryEffects(ctx *engine.Context) ([]engine.Effect, error) {
	if ctx.Intent == nil {
		return nil, fmt.Errorf("newEntry effects require a recorded invocation")
	}
	entryID := ctx.Intent.Values["entryId"]
	target := MutationTarget{Project: ProjectID(ctx.Intent.Values["project"]), Branch: ctx.Intent.Values["branch"]}
	state := "unknown"
	exists, err := w.app.entryPublicationExists(w.ctx, w.identity, target, w.newEntryPublicationKey(ctx.Intent), entryID)
	switch {
	case err != nil:
		slogutils.FromContext(w.ctx).Info("publication lookup for cancelled capture failed", "entry", entryID, "error", err)
	case exists:
		state = "published"
	default:
		state = "absent"
	}
	return []engine.Effect{{Kind: "entry", ID: entryID, State: state}}, nil
}

func (w *WorkflowSession) stagedAt(position uint64) (map[string]string, error) {
	_, _, stored, err := w.app.resolveSession(w.ctx, w.identity, w.ID())
	if err != nil {
		return nil, err
	}
	staged := make(map[string]string)
	for _, event := range stored.Events {
		if event.Sequence >= position {
			break
		}
		if event.Code != workflowStagedBlobCode {
			continue
		}
		var item struct {
			Handle string `json:"handle"`
			BlobID string `json:"blob_id"`
		}
		if err := json.Unmarshal(event.Payload, &item); err != nil {
			return nil, err
		}
		staged[item.Handle] = item.BlobID
	}
	return staged, nil
}

func (w *WorkflowSession) capturePreflightCurrent(ctx *engine.Context) (bool, error) {
	current, _, err := w.capturePreflightState(ctx)
	return current, err
}

func (w *WorkflowSession) verifyCapturePreflight(ctx *engine.Context) error {
	current, accepted, err := w.capturePreflightState(ctx)
	if err != nil {
		return err
	}
	if !current || !accepted {
		return fmt.Errorf("capture requires accepted preflight for its current input")
	}
	return nil
}

func (w *WorkflowSession) capturePreflightState(ctx *engine.Context) (bool, bool, error) {
	_, _, stored, err := w.app.resolveSession(w.ctx, w.identity, w.ID())
	if err != nil {
		return false, false, err
	}
	current, accepted := false, false
	checkedBranch := ""
	selected := workflowStoreStrings(ctx.Store, "attachments")
	staged := make(map[string]string)
	for _, event := range stored.Events {
		if event.Code == workflowStagedBlobCode {
			var item struct {
				Handle string `json:"handle"`
				BlobID string `json:"blob_id"`
			}
			if err := json.Unmarshal(event.Payload, &item); err != nil {
				return false, false, err
			}
			if slices.Contains(selected, item.Handle) && staged[item.Handle] != item.BlobID {
				current, accepted = false, false
			}
			staged[item.Handle] = item.BlobID
			continue
		}
		if event.Code != WorkflowEventCode {
			continue
		}
		var record engine.Event
		if err := json.Unmarshal(event.Payload, &record); err != nil {
			return false, false, err
		}
		if record.Instance != ctx.Instance {
			continue
		}
		switch record.Event {
		case engine.EventReport, engine.EventChooserAnswer:
			if fields, ok := record.Data["fields"].(map[string]any); ok {
				for name := range fields {
					if slices.Contains(captureDraftFields, name) {
						current, accepted = false, false
					}
				}
			}
		case engine.EventOpResult:
			name, _ := record.Data["fn"].(string)
			if name == "preflightEntry" {
				current, accepted = true, true
				writes, _ := record.Data["writes"].(map[string]any)
				checkedBranch, _ = writes["resolvedCaptureBranch"].(string)
				encoded, err := json.Marshal(writes["findings"])
				if err != nil {
					return false, false, err
				}
				var findings []Finding
				if err := json.Unmarshal(encoded, &findings); err != nil {
					return false, false, err
				}
				for _, finding := range findings {
					if finding.Severity == string(query.SeverityHigh) {
						accepted = false
					}
				}
			}
			if name == "recordOverride" && current {
				accepted = true
			}
		}
	}
	if !current {
		return false, false, nil
	}
	target, _ := w.effectiveTargetFor(w.instanceProject(ctx.Instance), ctx.Store)
	if target.Branch == "" {
		runtime, err := w.targetRuntime(target.Project, AccessWrite)
		if err != nil {
			return false, false, err
		}
		target, _, err = runtime.baseTarget(w.ctx)
		if err != nil {
			return false, false, err
		}
	}
	if checkedBranch != target.Branch {
		return false, false, nil
	}
	return current, accepted, nil
}
