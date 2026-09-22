package local

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	app "github.com/networkteam/sdd/pkg/application"
)

type retainedSnapshot struct {
	snapshot *app.Snapshot
	files    *zip.Reader
	leases   int
}

type snapshotAttachments struct {
	files fs.FS
	dir   string
}

func (s snapshotAttachments) ReadAttachmentPage(ctx context.Context, entry, name string, offset int64, limit int) (app.AttachmentPage, error) {
	if err := ctx.Err(); err != nil {
		return app.AttachmentPage{}, err
	}
	page, err := app.PageAttachment(s.files, ".", entry, name, offset, limit)
	if err != nil {
		return app.AttachmentPage{}, err
	}
	return attachmentPageWithLocalPath(page, s.dir, entry)
}

// AcquireSnapshot retains immutable graph and attachment bytes while a lease
// exists. Exact revisions survive concurrent Apply calls, not process restarts;
// durable consumers supply their own revision-backed SnapshotReader. Memory
// scales with all graph and attachment bytes in live revisions. A nonempty
// requested branch must match FilesystemGraphStoreOptions.Branch.
func (s *FilesystemGraphStore) AcquireSnapshot(ctx context.Context, q app.SnapshotReadQuery) (*app.AcquiredSnapshot, error) {
	if (q.Branch != "" && q.Branch != s.branch) || (q.ExactRevision != "" && q.IncludesRevision != "") {
		return nil, fmt.Errorf("sdd: invalid filesystem snapshot selection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.ExactRevision != "" {
		if retained := s.snapshots[q.ExactRevision]; retained != nil {
			return s.leaseSnapshot(retained), nil
		}
	}
	lock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	revision, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return nil, err
	}
	if q.ExactRevision != "" && q.ExactRevision != revision {
		return nil, fmt.Errorf("sdd: exact source revision is no longer retained")
	}
	if q.IncludesRevision != "" {
		ok, err := s.includesRevision(ctx, revision, q.IncludesRevision)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("sdd: current revision cannot be shown to include the requested write")
		}
	}
	if retained := s.snapshots[revision]; retained != nil {
		return s.leaseSnapshot(retained), nil
	}
	files, err := freezeGraphFS(ctx, s.dir)
	if err != nil {
		return nil, err
	}
	after, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return nil, err
	}
	if after != revision {
		return nil, fmt.Errorf("sdd: graph changed while acquiring snapshot")
	}
	snapshot, err := app.LoadSnapshotFS(ctx, s.project, revision, files, ".")
	if err != nil {
		return nil, err
	}
	retained := &retainedSnapshot{snapshot: snapshot, files: files}
	if s.snapshots == nil {
		s.snapshots = map[string]*retainedSnapshot{}
	}
	s.snapshots[revision] = retained
	return s.leaseSnapshot(retained), nil
}

func (s *FilesystemGraphStore) leaseSnapshot(retained *retainedSnapshot) *app.AcquiredSnapshot {
	retained.leases++
	var once sync.Once
	return &app.AcquiredSnapshot{Snapshot: retained.snapshot, Attachments: snapshotAttachments{files: retained.files, dir: s.dir}, Release: func() error {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			retained.leases--
			if retained.leases == 0 {
				delete(s.snapshots, retained.snapshot.Revision())
			}
		})
		return nil
	}}
}

// includesRevision reports whether the current revision carries the required
// one: equal revisions, or on a Git-backed target a commit that is an ancestor
// of the branch head.
func (s *FilesystemGraphStore) includesRevision(ctx context.Context, current, required string) (bool, error) {
	for node, seen := current, map[string]bool{}; node != "" && !seen[node]; node = s.lineage[node] {
		if node == required {
			return true, nil
		}
		seen[node] = true
	}
	if s.publicationGit == nil || !isGitRevision(required) {
		return false, nil
	}
	return s.publicationGit.isAncestor(ctx, required)
}

func isGitRevision(revision string) bool {
	if len(revision) < 7 || len(revision) > 64 {
		return false
	}
	for _, r := range revision {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func freezeGraphFS(ctx context.Context, dir string) (*zip.Reader, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	err := fs.WalkDir(os.DirFS(dir), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".sdd-runtime" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("sdd: snapshot contains a symbolic link: %s", path)
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		file, err := archive.CreateHeader(&zip.FileHeader{Name: path, Method: zip.Store})
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return err
	})
	if err := errors.Join(err, archive.Close()); err != nil {
		return nil, err
	}
	return zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
}

func attachmentPageWithLocalPath(page app.AttachmentPage, dir, entry string) (app.AttachmentPage, error) {
	relative, err := app.AttachmentDirRelPath(entry)
	if err != nil {
		return app.AttachmentPage{}, err
	}
	page.LocalPath, err = filepath.Abs(filepath.Join(dir, relative, page.Filename))
	return page, err
}
