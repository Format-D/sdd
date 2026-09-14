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
	return fmt.Sprintf("%v; %s", e.Err, e.Intent.RetryInstructions())
}
func (e *OperationError) Unwrap() error { return e.Err }

func (m MutationIntent) RetryInstructions() string {
	return fmt.Sprintf("retry with next(instance=%q, retry_ref=%d) without resending the report", m.Instance, m.Ref)
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
