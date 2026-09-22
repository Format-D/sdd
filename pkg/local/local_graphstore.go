package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	app "github.com/networkteam/sdd/pkg/application"
)

type FilesystemGraphStoreOptions struct {
	Project  app.ProjectID
	GraphDir string
	// Branch is the authority assigned by the target acquirer, if branch-scoped.
	Branch string
	// PublicationGit identifies a committed publication by the trailer its
	// finalizer wrote; the finalizer, not the store, commits. Without it,
	// publication recognizes retries by entry ID only.
	PublicationGit *GitFinalizer
}

// FilesystemGraphStore is the local canonical graph authority. It owns its
// revision cache and never requires callers to invalidate snapshots.
type FilesystemGraphStore struct {
	project        app.ProjectID
	branch         string
	dir            string
	mu             sync.Mutex
	snapshots      map[string]*retainedSnapshot
	publicationGit *GitFinalizer
	// lineage maps a revision this store published to the revision it replaced,
	// so a read can be shown to include an earlier write of this process.
	lineage map[string]string
}

func NewFilesystemGraphStore(options FilesystemGraphStoreOptions) (*FilesystemGraphStore, error) {
	if options.Project == "" {
		return nil, fmt.Errorf("sdd: filesystem graph project is required")
	}
	if options.GraphDir == "" {
		return nil, fmt.Errorf("sdd: filesystem graph directory is required")
	}
	if err := os.MkdirAll(options.GraphDir, 0o755); err != nil {
		return nil, fmt.Errorf("sdd: creating filesystem graph directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(options.GraphDir, ".sdd-runtime"), 0o755); err != nil {
		return nil, fmt.Errorf("sdd: creating graph runtime directory: %w", err)
	}
	return &FilesystemGraphStore{
		project:        options.Project,
		branch:         options.Branch,
		dir:            options.GraphDir,
		publicationGit: options.PublicationGit,
	}, nil
}

func (s *FilesystemGraphStore) Current(ctx context.Context) (*app.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	return s.currentLocked(ctx)
}

func (s *FilesystemGraphStore) currentLocked(ctx context.Context) (*app.Snapshot, error) {
	revision, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return nil, err
	}
	return app.LoadSnapshotFS(ctx, s.project, revision, os.DirFS(s.dir), ".")
}

func (s *FilesystemGraphStore) ReadAttachmentPage(_ context.Context, entryID, filename string, offset int64, maxBytes int) (app.AttachmentPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.AttachmentPage{}, err
	}
	defer unlock(lock)
	page, err := app.PageAttachment(os.DirFS(s.dir), ".", entryID, filename, offset, maxBytes)
	if err != nil {
		return app.AttachmentPage{}, err
	}
	return attachmentPageWithLocalPath(page, s.dir, entryID)
}

func (s *FilesystemGraphStore) lock() (*flock.Flock, error) { return lockGraph(s.dir) }

// lockGraph serializes every writer of one graph directory, the store and the
// Git finalizer alike.
func lockGraph(dir string) (*flock.Flock, error) {
	runtimeDir := filepath.Join(dir, ".sdd-runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(runtimeDir, "graph.lock"))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return lock, nil
}

func graphDirectoryRevision(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() && entry.Name() == ".sdd-runtime" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			files = append(files, filename)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, filename := range files {
		rel, err := filepath.Rel(dir, filename)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// validateGraphPath refuses paths a publication may not target: outside the
// graph directory or inside its runtime directory.
func validateGraphPath(root, logicalPath string) error {
	if logicalPath == ".sdd-runtime" || strings.HasPrefix(logicalPath, ".sdd-runtime/") {
		return fmt.Errorf("sdd: canonical mutation cannot target runtime path %q", logicalPath)
	}
	_, err := safeGraphPath(root, logicalPath)
	return err
}

func syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}

func safeGraphPath(root, logicalPath string) (string, error) {
	if logicalPath == "" || strings.Contains(logicalPath, "\\") || !fs.ValidPath(logicalPath) {
		return "", fmt.Errorf("sdd: invalid graph logical path %q", logicalPath)
	}
	target := filepath.Join(root, filepath.FromSlash(logicalPath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sdd: graph path escapes root: %q", logicalPath)
	}
	return target, nil
}
