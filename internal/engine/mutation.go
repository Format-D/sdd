package engine

import (
	"encoding/json"
	"fmt"
	"maps"
)

// MutationIntent identifies one invocation without duplicating its recorded input.
type MutationIntent struct {
	Ref      uint64
	Instance string
	Step     string
	Command  string
	Values   map[string]string
}

// OperationError carries the exact continuation of a durably recorded invocation.
type OperationError struct {
	Intent MutationIntent
	Err    error
}

func (e *OperationError) Error() string {
	return fmt.Sprintf("%v; %s", e.Err, e.Intent.ContinuationInstructions())
}
func (e *OperationError) Unwrap() error { return e.Err }

func (m MutationIntent) ContinuationInstructions() string {
	return fmt.Sprintf("operation %q recorded values %v; retry with next(instance=%q, retry_ref=%d) or cancel with next(instance=%q, cancel_ref=%d), without resending the report. Retry uses the recorded input and identifiers. Cancel leaves existing or uncertain effects in place.", m.Command, m.Values, m.Instance, m.Ref, m.Instance, m.Ref)
}

const MutationCancelled = "cancelled"

// MutationCancellation records where an unfinished invocation returned without undoing effects.
type MutationCancellation struct {
	Intent     MutationIntent
	ReturnStep string
}

// CancelledMutation returns the latest invocation's cancellation, if it was cancelled.
func (s *Session) CancelledMutation() *MutationCancellation {
	if s.cancelled == nil {
		return nil
	}
	result := *s.cancelled
	result.Intent.Values = maps.Clone(result.Intent.Values)
	return &result
}

// PendingMutation returns the unresolved invocation, without executing it.
func (s *Session) PendingMutation() *MutationIntent {
	if s.intent == nil || s.intentDone {
		return nil
	}
	result := *s.intent
	result.Values = maps.Clone(s.intent.Values)
	return &result
}

func (s *Session) pendingError() error {
	return &OperationError{Intent: *s.intent, Err: fmt.Errorf("the session has unfinished operation %s", s.intent.Command)}
}

func (s *Session) checkProgression() error {
	if err := s.checkSink(); err != nil {
		return err
	}
	if s.PendingMutation() != nil {
		return s.pendingError()
	}
	return nil
}

func (s *Session) commandError(intent *MutationIntent, err error) error {
	if sinkErr := s.checkSink(); sinkErr != nil {
		return sinkErr
	}
	if intent == nil {
		return err
	}
	return &OperationError{Intent: *intent, Err: err}
}

// Retry continues the latest recorded invocation and serves its resulting position.
func (s *Session) Retry(instance string, ref uint64) (*Serve, error) {
	if err := s.checkSink(); err != nil {
		return nil, err
	}
	if s.intent == nil || s.intent.Ref != ref || s.intent.Instance != instance {
		return nil, fmt.Errorf("retry_ref %d is not the session's latest invocation for %s; resume the session", ref, instance)
	}
	if s.cancelled != nil {
		return nil, fmt.Errorf("operation %d was cancelled; its effects were left in place, and it cannot be retried", ref)
	}
	inst, ok := s.Instance(instance)
	if !ok {
		return nil, fmt.Errorf("instance %q not found", instance)
	}
	if !s.intentDone {
		if err := s.runCommand(inst, s.intent.Command); err != nil {
			return nil, err
		}
		inst.opDone = true
	}
	if err := s.cascade(inst); err != nil {
		return nil, err
	}
	return s.serve(inst)
}

// Cancel records the outcome and return position atomically, without running commands.
func (s *Session) Cancel(instance string, ref uint64) (*Serve, error) {
	if err := s.checkSink(); err != nil {
		return nil, err
	}
	if s.intent == nil || s.intent.Ref != ref || s.intent.Instance != instance {
		return nil, fmt.Errorf("cancel_ref %d is not the session's latest invocation for %s; resume the session", ref, instance)
	}
	inst, ok := s.Instance(instance)
	if !ok {
		return nil, fmt.Errorf("instance %q not found", instance)
	}
	if s.cancelled == nil {
		if s.intentDone {
			return nil, fmt.Errorf("operation %d already completed and cannot be cancelled", ref)
		}
		s.appendEvent(instance, EventMutationOutcome, map[string]any{
			"intent_ref": ref, "step": s.intent.Step, "fn": s.intent.Command,
			"outcome": MutationCancelled, "return_step": inst.interactionStep,
		})
		if err := s.checkSink(); err != nil {
			return nil, err
		}
		s.applyCancellation(inst, inst.interactionStep)
	}
	return s.serveWith(inst, true)
}

func (s *Session) applyCancellation(inst *Instance, returnStep string) {
	s.cancelled = &MutationCancellation{Intent: *s.intent, ReturnStep: returnStep}
	s.intentDone = true
	inst.opDone = false
	if returnStep == "" {
		inst.Status, inst.Outcome = StatusAbandoned, MutationCancelled
		return
	}
	inst.Step = returnStep
}

func (s *Session) restoreIntent(event Event) error {
	if s.PendingMutation() != nil {
		return fmt.Errorf("another mutation intent is already pending")
	}
	inst, err := s.replayInstance(event)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	var data struct {
		Step    string            `json:"step"`
		Command string            `json:"fn"`
		Values  map[string]string `json:"values"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if event.Position == 0 || data.Step != inst.Step || data.Command == "" {
		return fmt.Errorf("mutation intent has an invalid position or invocation")
	}
	s.intent = &MutationIntent{Ref: event.Position, Instance: inst.ID, Step: data.Step, Command: data.Command, Values: data.Values}
	s.intentStore = inst.Store.Clone()
	s.intentDone = false
	s.cancelled = nil
	return nil
}

func (s *Session) validateOutcome(event Event) error {
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	var data struct {
		Ref     uint64 `json:"intent_ref"`
		Step    string `json:"step"`
		Command string `json:"fn"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if s.intent == nil || s.intentDone || data.Ref != s.intent.Ref || event.Instance != s.intent.Instance || data.Step != s.intent.Step || data.Command != s.intent.Command {
		return fmt.Errorf("mutation outcome does not match its pending intent")
	}
	return nil
}
