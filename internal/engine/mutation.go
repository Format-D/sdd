package engine

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// MutationIntent identifies one invocation without duplicating its recorded input.
type MutationIntent struct {
	Ref      uint64
	Instance string
	Step     string
	Command  string
	Values   map[string]string
	// To is the transition the dispatching chooser option owes once the
	// command finishes; a retry completes it, so the instance never re-serves
	// the answered chooser (s-tac-do6). Empty for a step op.
	To string
}

// OperationError is a recorded invocation that did not finish; the intent stays
// pending and Err is this attempt's diagnosis. Served as a position, not an
// error (d-tac-qws).
type OperationError struct {
	Intent MutationIntent
	Err    error
}

func (e *OperationError) Error() string {
	return fmt.Sprintf("operation %q did not finish: %v", e.Intent.Command, e.Err)
}
func (e *OperationError) Unwrap() error { return e.Err }

// PendingOperationError refuses any transition but retry or cancel while an
// operation is unfinished (d-tac-t6u).
type PendingOperationError struct {
	Intent MutationIntent
}

func (e *PendingOperationError) Error() string {
	return fmt.Sprintf("operation %q is unfinished; only its retry or cancellation is accepted", e.Intent.Command)
}

// Effect is one thing a recorded invocation left, in the operation's own
// vocabulary (d-tac-7mh).
type Effect struct {
	Kind  string
	ID    string
	State string
}

// OperationEffects asks the intent's command what it left; a command without
// a report leaves only its recorded values.
func (s *Session) OperationEffects(intent MutationIntent) ([]Effect, error) {
	cmd, ok := s.engine.Registry.Command(intent.Command)
	if !ok {
		return nil, fmt.Errorf("operation %q is not a registered command", intent.Command)
	}
	if cmd.Effects == nil {
		return nil, nil
	}
	inst, ok := s.Instance(intent.Instance)
	if !ok {
		return nil, fmt.Errorf("instance %q not found", intent.Instance)
	}
	intent.Values = maps.Clone(intent.Values)
	return cmd.Effects(&Context{Instance: inst.ID, Intent: &intent, Store: inst.Store, Step: intent.Step, Reads: s.reads})
}

// The pending-operation serve's goal and its only prose, fixed by d-tac-qws and d-tac-ft4.
const (
	PendingOperationGoal         = "retry the unfinished operation, or cancel it to return to the preceding interaction"
	PendingOperationInstructions = "This operation did not finish. Tell the user in a sentence. Retry, saying so, when trying again can help. When it cannot, or has stopped helping, put both choices to the user: retry, or cancel. It can stay pending until they choose. Retry is safe: it reuses the recorded input, and effects already applied are recognized, not repeated. Cancel leaves what exists in place."
)

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

// clearCancellation ends a cancellation's currency: the next interaction on
// its instance, a report or an answer, moves the dialogue on (d-tac-t6u).
func (s *Session) clearCancellation(instanceID string) {
	if s.cancelled != nil && s.cancelled.Intent.Instance == instanceID {
		s.cancelled = nil
	}
}

// CancellationServe is a current cancellation as a serve carries it: the
// intent it ended, where the instance returned to, and the effects the
// operation reports it left (d-tac-7mh).
type CancellationServe struct {
	Intent     MutationIntent
	ReturnStep string
	Closed     bool
	Effects    []Effect
}

// Cancellation returns the current cancellation with its operation's live
// effects report, or nil when none is current.
func (s *Session) Cancellation() (*CancellationServe, error) {
	cancelled := s.CancelledMutation()
	if cancelled == nil {
		return nil, nil
	}
	effects, err := s.OperationEffects(cancelled.Intent)
	if err != nil {
		return nil, fmt.Errorf("reporting effects of cancelled operation %q: %w", cancelled.Intent.Command, err)
	}
	return &CancellationServe{Intent: cancelled.Intent, ReturnStep: cancelled.ReturnStep, Closed: cancelled.ReturnStep == "", Effects: effects}, nil
}

// CancellationNotice is the cancellation serve's prose: where the instance
// returned to and what the operation reports it left.
func CancellationNotice(c *CancellationServe) string {
	position := fmt.Sprintf("Returned to the %q interaction without advancing it.", c.ReturnStep)
	if c.Closed {
		position = "Closed the instance, because no interaction preceded the operation."
	}
	left := "What it already applied stays in place."
	if len(c.Effects) > 0 {
		items := make([]string, 0, len(c.Effects))
		for _, effect := range c.Effects {
			items = append(items, fmt.Sprintf("%s %s, %s", effect.Kind, effect.ID, effect.State))
		}
		left = "It left: " + strings.Join(items, "; ") + "."
	}
	return fmt.Sprintf("Cancelled operation %q. %s %s Nothing is cleaned up. Confirming again starts a new operation with new identifiers.", c.Intent.Command, position, left)
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
	return &PendingOperationError{Intent: *s.intent}
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
		to := s.intent.To
		if err := s.runCommand(inst, s.intent.Command, to); err != nil {
			return nil, err
		}
		inst.opDone = true
		if to != "" {
			if err := s.transitionTo(inst, to, false); err != nil {
				return nil, err
			}
		}
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
		To      string            `json:"to"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if event.Position == 0 || data.Step != inst.Step || data.Command == "" {
		return fmt.Errorf("mutation intent has an invalid position or invocation")
	}
	s.intent = &MutationIntent{Ref: event.Position, Instance: inst.ID, Step: data.Step, Command: data.Command, Values: data.Values, To: data.To}
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
