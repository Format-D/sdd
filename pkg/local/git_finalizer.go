package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	app "github.com/networkteam/sdd/pkg/application"
)

// GitFinalizer commits only the graph paths in one applied mutation, bound to
// the acquired checkout rather than process cwd.
type GitFinalizer struct {
	Checkout string
	GraphDir string
	Branch   string
	Timeout  time.Duration
}

func (GitFinalizer) Name() string { return "git" }

func (f GitFinalizer) Finalize(ctx context.Context, mutation app.AppliedMutation) error {
	runtimeDir := filepath.Join(f.Checkout, f.GraphDir, ".sdd-runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("git finalizer runtime directory: %w", err)
	}
	lock := flock.New(filepath.Join(runtimeDir, "graph.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer unlock(lock)
	return f.finalizeLocked(ctx, mutation)
}

func (f GitFinalizer) lookupRevision(ctx context.Context, mutationID string) (string, error) {
	if strings.TrimSpace(f.Branch) == "" {
		return "", fmt.Errorf("git finalizer: concrete branch is required")
	}
	trailerPattern := "^" + regexp.QuoteMeta("SDD-Mutation: "+mutationID) + "$"
	out, err := exec.CommandContext(ctx, "git", "-C", f.Checkout, "log", f.Branch, "--extended-regexp", "--grep="+trailerPattern, "--format=%H", "-n", "1").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git finalizer log: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (f GitFinalizer) finalizeLocked(ctx context.Context, mutation app.AppliedMutation) error {
	if f.Timeout <= 0 {
		f.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	trailer := "SDD-Mutation: " + mutation.BatchID
	revision, err := f.lookupRevision(ctx, mutation.BatchID)
	if err != nil {
		return err
	}
	if revision != "" {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	addPath := func(logical string) {
		if logical == "" {
			return
		}
		path := filepath.Join(f.GraphDir, filepath.FromSlash(logical))
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, change := range mutation.Batch.Changes {
		addPath(change.LogicalPath)
	}
	for _, attachment := range mutation.Batch.Attachments {
		addPath(attachment.LogicalPath)
	}
	if len(paths) == 0 {
		return fmt.Errorf("git finalizer: mutation %s has no paths", mutation.BatchID)
	}
	addArgs := append([]string{"-C", f.Checkout, "add", "--all", "--"}, paths...)
	if out, err := exec.CommandContext(ctx, "git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git finalizer add: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	message := mutation.Batch.Message
	if message == "" {
		message = "sdd: apply " + mutation.BatchID
	}
	commitArgs := append([]string{"-C", f.Checkout, "commit", "-m", message + "\n\n" + trailer, "--"}, paths...)
	if out, err := exec.CommandContext(ctx, "git", commitArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git finalizer commit: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}
