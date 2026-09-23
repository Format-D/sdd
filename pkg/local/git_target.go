package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	app "github.com/networkteam/sdd/pkg/application"
)

type TargetRuntimeFactory func(context.Context, string, app.MutationTarget) (app.GraphStore, []app.MutationFinalizer, func() error, error)

type GitWorktreeAcquirerOptions struct {
	Project        app.ProjectID
	ServerCheckout string
	Factory        TargetRuntimeFactory
	// ReadFactory opens a read source without constructing mutation adapters.
	ReadFactory func(context.Context, string, app.SnapshotReadQuery) (*app.AcquiredSnapshot, error)
}

// GitWorktreeAcquirer rediscoveries one registered checkout for every
// acquisition. It persists branch authority, never checkout paths.
type GitWorktreeAcquirer struct {
	project        app.ProjectID
	serverCheckout string
	factory        TargetRuntimeFactory
	readFactory    func(context.Context, string, app.SnapshotReadQuery) (*app.AcquiredSnapshot, error)
	runGit         func(context.Context, ...string) ([]byte, error)
}

func NewGitWorktreeAcquirer(options GitWorktreeAcquirerOptions) (*GitWorktreeAcquirer, error) {
	if options.Project == "" || strings.TrimSpace(options.ServerCheckout) == "" || (options.Factory == nil && options.ReadFactory == nil) {
		return nil, fmt.Errorf("sdd: local target project, server checkout, and factory are required")
	}
	root, err := filepath.Abs(options.ServerCheckout)
	if err != nil {
		return nil, err
	}
	return &GitWorktreeAcquirer{project: options.Project, serverCheckout: root, factory: options.Factory, readFactory: options.ReadFactory, runGit: runGitTargetCommand}, nil
}

func runGitTargetCommand(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "git", args...).CombinedOutput()
}

func (a *GitWorktreeAcquirer) Acquire(ctx context.Context, target app.MutationTarget) (*app.AcquiredTarget, error) {
	if a.factory == nil {
		return nil, fmt.Errorf("sdd: mutation acquisition is not configured")
	}
	checkout, err := a.resolveCheckout(ctx, target)
	if err != nil {
		return nil, err
	}
	graph, finalizers, release, err := a.factory(ctx, checkout, target)
	if err != nil {
		return nil, err
	}
	if release == nil {
		release = func() error { return nil }
	}
	return &app.AcquiredTarget{Target: target, Graph: graph, Finalizers: finalizers, Release: release}, nil
}

// AcquireSnapshot resolves a registered read branch without mutation acquisition.
// Empty branch reads the server checkout. Exact-source availability belongs to
// ReadFactory; this resolver does not retain historical revisions.
func (a *GitWorktreeAcquirer) AcquireSnapshot(ctx context.Context, q app.SnapshotReadQuery) (*app.AcquiredSnapshot, error) {
	if a.readFactory == nil {
		return nil, fmt.Errorf("sdd: snapshot acquisition is not configured")
	}
	checkout := a.serverCheckout
	if q.Branch != "" {
		var err error
		checkout, err = a.resolveCheckout(ctx, app.MutationTarget{Project: a.project, Branch: q.Branch})
		if err != nil {
			return nil, err
		}
	}
	return a.readFactory(ctx, checkout, q)
}

// BaseBranch answers the branch the serving checkout has checked out at the
// moment of the call — the unbound session's base (20260923-233057-d-cpt-ekd).
// It is empty when the checkout has none: a detached HEAD, or a directory
// outside any Git repository.
func (a *GitWorktreeAcquirer) BaseBranch(ctx context.Context) (string, error) {
	output, err := a.runGit(ctx, "-C", a.serverCheckout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 1 || strings.Contains(string(output), "not a git repository")) {
			return "", nil
		}
		return "", fmt.Errorf("sdd: reading the serving checkout's branch: %s (%w)", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Project is the project whose checkouts the acquirer resolves.
func (a *GitWorktreeAcquirer) Project() app.ProjectID { return a.project }

// ValidateBranch applies the live acquisition rule without opening graph
// adapters or finalizers.
func (a *GitWorktreeAcquirer) ValidateBranch(ctx context.Context, target app.MutationTarget) error {
	_, err := a.resolveCheckout(ctx, target)
	return err
}

func (a *GitWorktreeAcquirer) resolveCheckout(ctx context.Context, target app.MutationTarget) (string, error) {
	if err := target.Validate(a.project); err != nil {
		return "", err
	}
	if output, err := a.runGit(ctx, "check-ref-format", "--branch", target.Branch); err != nil {
		return "", fmt.Errorf("sdd: invalid mutation target branch %q: %s (%w)", target.Branch, strings.TrimSpace(string(output)), err)
	}
	output, err := a.runGit(ctx, "-C", a.serverCheckout, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("sdd: listing registered worktrees: %w", err)
	}
	matches := matchingWorktrees(output, "refs/heads/"+target.Branch)
	if len(matches) != 1 {
		return "", fmt.Errorf("sdd: mutation target branch %q must have exactly one registered checkout (found %d)", target.Branch, len(matches))
	}
	checkout := matches[0]
	head, err := a.runGit(ctx, "-C", checkout, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", fmt.Errorf("sdd: mutation target checkout %q is detached or unreadable: %w", checkout, err)
	}
	if actual := strings.TrimSpace(string(head)); actual != "refs/heads/"+target.Branch {
		return "", fmt.Errorf("sdd: mutation target checkout HEAD changed: got %q, want %q", actual, "refs/heads/"+target.Branch)
	}
	return checkout, nil
}

func matchingWorktrees(output []byte, branchRef string) []string {
	var matches []string
	var path, branch string
	flush := func() {
		if path != "" && branch == branchRef {
			matches = append(matches, path)
		}
		path, branch = "", ""
	}
	for _, raw := range bytes.Split(output, []byte{0}) {
		line := string(raw)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			if path != "" {
				flush()
			}
			path = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branch = strings.TrimPrefix(line, "branch ")
		}
	}
	flush()
	return matches
}
