package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MutationTarget is the immutable canonical authority for one graph
// mutation. Project identifies the project; Branch names its logical write
// authority. Adapters resolve that authority to their storage.
type MutationTarget struct {
	Project ProjectID `json:"project"`
	Branch  string    `json:"branch"`
}

func (t MutationTarget) Validate(project ProjectID) error {
	if t.Project == "" || t.Project != project {
		return &ApplicationError{Code: ErrorWriteDenied, Message: "mutation target project must equal the session project"}
	}
	if strings.TrimSpace(t.Branch) == "" || t.Branch != strings.TrimSpace(t.Branch) {
		return &ApplicationError{Code: ErrorWriteDenied, Message: "mutation target branch must be concrete and non-empty"}
	}
	return nil
}

// AcquiredTarget contains target-scoped adapters for one short operation.
// Release is mandatory and is called on every success and failure path.
type AcquiredTarget struct {
	Target     MutationTarget
	Graph      GraphStore
	Finalizers []MutationFinalizer
	Release    func() error
}

func (a *AcquiredTarget) validate(requested MutationTarget) error {
	if a == nil || a.Graph == nil || a.Release == nil {
		return fmt.Errorf("sdd: target acquisition returned an incomplete runtime")
	}
	if a.Target != requested {
		return fmt.Errorf("sdd: target acquisition returned %+v for requested %+v", a.Target, requested)
	}
	return nil
}

// TargetAcquirer resolves a concrete mutation authority to short-lived,
// target-scoped graph and finalizer adapters. Implementations rediscover the
// target on every call; checkout paths are not durable intent.
type TargetAcquirer interface {
	Acquire(context.Context, MutationTarget) (*AcquiredTarget, error)
}

// TargetAcquirerFunc adapts a function to TargetAcquirer.
type TargetAcquirerFunc func(context.Context, MutationTarget) (*AcquiredTarget, error)

func (f TargetAcquirerFunc) Acquire(ctx context.Context, target MutationTarget) (*AcquiredTarget, error) {
	return f(ctx, target)
}

// BranchValidator resolves branch authority without opening graph adapters or
// finalizers. It is the declare-time half of TargetAcquirer: local
// compositions use the same live checkout rule for both.
type BranchValidator interface {
	ValidateBranch(context.Context, MutationTarget) error
}

// BranchValidatorFunc adapts a function to BranchValidator.
type BranchValidatorFunc func(context.Context, MutationTarget) error

func (f BranchValidatorFunc) ValidateBranch(ctx context.Context, target MutationTarget) error {
	return f(ctx, target)
}

// BaseBranchResolver names the branch a composition derives as an unbound
// session's base (20260923-233057-d-cpt-ekd). A local composition answers the
// branch its serving checkout has checked out at the moment of the call; an
// empty answer means it has none, and the configured default branch applies. A composition without one uses the configured default.
type BaseBranchResolver interface {
	BaseBranch(context.Context) (string, error)
}

// BaseSource says where an unbound session's base branch came from.
type BaseSource string

const (
	BaseFromCheckout  BaseSource = "the serving checkout's branch"
	BaseWithoutBranch BaseSource = "configured default; the serving checkout has no branch"
	BaseFromDefault   BaseSource = "configured default"
)

// targetAcquisitionError marks failures from the one shared target-acquisition
// boundary without changing their public message or typed cause. Workflow
// routing uses the marker to add session-binding provenance only when the
// session binding actually supplied the target.
type targetAcquisitionError struct {
	target MutationTarget
	cause  error
}

func (e *targetAcquisitionError) Error() string { return e.cause.Error() }
func (e *targetAcquisitionError) Unwrap() error { return e.cause }

func markTargetAcquisitionError(target MutationTarget, cause error) error {
	if cause == nil {
		return nil
	}
	var marked *targetAcquisitionError
	if errors.As(cause, &marked) {
		return cause
	}
	return &targetAcquisitionError{target: target, cause: cause}
}

// withSessionBindingTargetError adds routing provenance only when the failed
// target came from a durable session declaration. Explicit branch reads and
// procedure-owned targets deliberately retain the underlying acquisition
// error unchanged.
func withSessionBindingTargetError(branch string, fromBinding bool, err error) error {
	if err == nil || !fromBinding {
		return err
	}
	var acquisition *targetAcquisitionError
	if !errors.As(err, &acquisition) || acquisition.target.Branch != branch {
		return err
	}
	return fmt.Errorf("session is bound to branch %q and acquiring that branch failed; check that branch out and retry, or, if the binding is stale, re-declare the binding or clear it: %w", branch, err)
}

// withBaseTargetError names the remedies when the session's derived base
// branch cannot be acquired: a checkout of it, or a declared branch. An empty
// branch is the base as the failed acquisition resolved it.
func withBaseTargetError(branch string, err error) error {
	var acquisition *targetAcquisitionError
	if err == nil || !errors.As(err, &acquisition) || (branch != "" && acquisition.target.Branch != branch) {
		return err
	}
	branch = acquisition.target.Branch
	return fmt.Errorf("the session's base branch %q has no usable checkout; check that branch out and retry, or declare the branch you work on through the session branch-binding capability, then cancel and redo the write: %w", branch, err)
}

// FixedTargetAcquirer is a small composition adapter for stores whose one
// configured runtime already represents a concrete target. It exact-matches
// the target and never interprets cwd.
type FixedTargetAcquirer struct {
	Target     MutationTarget
	Graph      GraphStore
	Finalizers []MutationFinalizer
}

func (a FixedTargetAcquirer) Acquire(_ context.Context, target MutationTarget) (*AcquiredTarget, error) {
	if target != a.Target {
		return nil, &ApplicationError{Code: ErrorWriteDenied, Message: "requested mutation target is not available"}
	}
	return &AcquiredTarget{
		Target: target, Graph: a.Graph, Finalizers: append([]MutationFinalizer(nil), a.Finalizers...),
		Release: func() error { return nil },
	}, nil
}
