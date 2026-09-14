package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	if f.Timeout <= 0 {
		f.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	revision, err := f.lookupTrailer(ctx, "SDD-Publication: "+mutation.BatchID)
	if err != nil {
		return err
	}
	if revision != "" {
		return nil
	}
	if published, err := f.matchesLegacyPublication(ctx, mutation); err != nil || published {
		return err
	}
	return f.finalizeLocked(ctx, mutation, "SDD-Mutation: "+mutation.BatchID)
}

func (f GitFinalizer) matchesLegacyPublication(ctx context.Context, mutation app.AppliedMutation) (bool, error) {
	if !strings.HasPrefix(mutation.BatchID, "v1:") || mutation.Revision == "" {
		return false, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", f.Checkout, "show", "--no-patch", "--format=%B", mutation.Revision, "--").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git finalizer publication revision: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		legacy, ok := strings.CutPrefix(line, "SDD-Mutation: ")
		if !ok {
			continue
		}
		parts := strings.Split(legacy, "/")
		if len(parts) != 3 {
			continue
		}
		sequence, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		key := app.PublicationKey{Session: app.SessionID(parts[0]), Sequence: sequence, Discriminator: parts[2]}
		if key.Validate() == nil && key.String() == mutation.BatchID {
			return true, nil
		}
	}
	return false, nil
}

func (f GitFinalizer) lookupTrailer(ctx context.Context, trailer string) (string, error) {
	if strings.TrimSpace(f.Branch) == "" {
		return "", fmt.Errorf("git finalizer: concrete branch is required")
	}
	trailerPattern := "^" + regexp.QuoteMeta(trailer) + "$"
	out, err := exec.CommandContext(ctx, "git", "-C", f.Checkout, "log", f.Branch, "--extended-regexp", "--grep="+trailerPattern, "--format=%H", "-n", "1").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git finalizer log: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (f GitFinalizer) finalizeLocked(ctx context.Context, mutation app.AppliedMutation, trailer string) error {
	if f.Timeout <= 0 {
		f.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	revision, err := f.lookupTrailer(ctx, trailer)
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
