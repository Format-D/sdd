package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/networkteam/sdd/internal/model"
	app "github.com/networkteam/sdd/pkg/application"
)

func (s *FilesystemGraphStore) LookupEntryPublication(ctx context.Context, key app.PublicationKey, entryID string) (app.EntryPublication, bool, error) {
	if err := key.Validate(); err != nil {
		return app.EntryPublication{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	defer unlock(lock)
	return s.lookupEntryPublicationLocked(ctx, key, entryID)
}

func (s *FilesystemGraphStore) lookupEntryPublicationLocked(ctx context.Context, key app.PublicationKey, entryID string) (app.EntryPublication, bool, error) {
	logicalPath, err := model.IDToRelPath(entryID)
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	if s.publicationGit == nil {
		return s.readExistingEntry(ctx, logicalPath)
	}
	revision, err := s.publicationGit.lookupTrailer(ctx, mutationTrailer(key.String()))
	if err != nil || revision == "" {
		return app.EntryPublication{}, false, err
	}
	publication, err := s.readCommittedEntry(ctx, revision, logicalPath)
	return publication, err == nil, err
}

// PublishEntry writes the entry and its attachments once; a Git-backed target
// counts the publication as existing only after its finalizer committed it.
func (s *FilesystemGraphStore) PublishEntry(ctx context.Context, key app.PublicationKey, batch app.MutationBatch, blobs app.StagedBlobReader) (_ app.EntryPublication, err error) {
	if err := key.Validate(); err != nil {
		return app.EntryPublication{}, err
	}
	if len(batch.Changes) != 1 || batch.Changes[0].Delete || batch.Changes[0].Document == nil {
		return app.EntryPublication{}, fmt.Errorf("entry publication requires one complete entry document")
	}
	change := batch.Changes[0]
	entryID, err := model.RelPathToID(change.LogicalPath)
	if err != nil {
		return app.EntryPublication{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.EntryPublication{}, err
	}
	defer unlock(lock)
	if publication, exists, err := s.lookupEntryPublicationLocked(ctx, key, entryID); err != nil || exists {
		return publication, err
	}
	publication, exists, err := s.readExistingEntry(ctx, change.LogicalPath)
	if err != nil {
		return app.EntryPublication{}, err
	}
	if !exists {
		root, openErr := os.OpenRoot(s.dir)
		if openErr != nil {
			return app.EntryPublication{}, openErr
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		attachmentDir, err := model.AttachDirRelPath(entryID)
		if err != nil {
			return app.EntryPublication{}, err
		}
		for _, attachment := range batch.Attachments {
			if path.Dir(attachment.LogicalPath) != attachmentDir || !fs.ValidPath(attachment.LogicalPath) {
				return app.EntryPublication{}, fmt.Errorf("attachment path does not belong to entry %s", entryID)
			}
			if blobs == nil || attachment.BlobID == "" {
				return app.EntryPublication{}, fmt.Errorf("staged attachment source is missing")
			}
			reader, err := blobs.Open(ctx, attachment.BlobID)
			if err != nil {
				return app.EntryPublication{}, err
			}
			copyErr := publishFile(ctx, root, attachment.LogicalPath, reader)
			if err := errors.Join(copyErr, reader.Close()); err != nil {
				return app.EntryPublication{}, err
			}
		}
		before, err := graphDirectoryRevision(s.dir)
		if err != nil {
			return app.EntryPublication{}, err
		}
		if err := publishFile(ctx, root, change.LogicalPath, bytes.NewReader(change.CanonicalBytes)); err != nil {
			return app.EntryPublication{}, err
		}
		publication, _, err = s.readExistingEntry(ctx, change.LogicalPath)
		if err != nil {
			return app.EntryPublication{}, err
		}
		s.recordLineage(before, publication.Revision)
	}
	return publication, nil
}

// committedOnBranch reports whether the publication branch carries the path.
// Without Git nothing is known to be complete, so a present document is
// handed to the finalizers again; they are idempotent.
func (s *FilesystemGraphStore) committedOnBranch(ctx context.Context, logicalPath string) (bool, error) {
	if s.publicationGit == nil {
		return false, nil
	}
	_, committed, err := s.publicationGit.readCommittedFile(ctx, s.publicationGit.Branch, logicalPath)
	return committed, err
}

// recordLineage remembers which revision a publication advanced from, in
// memory and in the lineage file; it runs under the graph lock. A failure to
// append is not a failed publication: the write is on disk, and only a later
// includes-revision read loses its proof.
func (s *FilesystemGraphStore) recordLineage(before, after string) {
	if before == "" || after == "" || before == after {
		return
	}
	if s.lineage == nil {
		s.lineage = map[string]string{}
	}
	s.lineage[after] = before
	file, err := os.OpenFile(filepath.Join(s.dir, filepath.FromSlash(lineageFile)), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "%s %s\n", after, before)
	_ = file.Close()
}

// loadLineage merges the lineage file into the in-memory map.
func (s *FilesystemGraphStore) loadLineage() error {
	raw, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(lineageFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.lineage == nil {
		s.lineage = map[string]string{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		after, before, ok := strings.Cut(line, " ")
		if ok && after != "" && before != "" {
			s.lineage[after] = before
		}
	}
	return nil
}

func (s *FilesystemGraphStore) readExistingEntry(ctx context.Context, logicalPath string) (_ app.EntryPublication, _ bool, err error) {
	if err := ctx.Err(); err != nil {
		return app.EntryPublication{}, false, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	raw, err := root.ReadFile(logicalPath)
	if errors.Is(err, fs.ErrNotExist) {
		return app.EntryPublication{}, false, nil
	}
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	document, err := app.ParseEntryDocument(logicalPath, raw)
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	attachmentDir := strings.TrimSuffix(logicalPath, ".md")
	files, err := fs.ReadDir(root.FS(), attachmentDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return app.EntryPublication{}, false, err
	}
	for _, file := range files {
		if !file.IsDir() {
			document.Attachments = append(document.Attachments, file.Name())
		}
	}
	revision, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return app.EntryPublication{}, false, err
	}
	return app.EntryPublication{Revision: revision, Document: document}, true, nil
}

func (s *FilesystemGraphStore) readCommittedEntry(ctx context.Context, revision, logicalPath string) (app.EntryPublication, error) {
	filename := path.Join(s.publicationGit.GraphDir, logicalPath)
	raw, err := exec.CommandContext(ctx, "git", "-C", s.publicationGit.Checkout, "show", revision+":"+filename).Output()
	if err != nil {
		return app.EntryPublication{}, fmt.Errorf("reading published entry at %s: %w", revision, err)
	}
	document, err := app.ParseEntryDocument(logicalPath, raw)
	if err != nil {
		return app.EntryPublication{}, err
	}
	attachmentDir := strings.TrimSuffix(filename, ".md")
	files, err := exec.CommandContext(ctx, "git", "-C", s.publicationGit.Checkout, "ls-tree", "-r", "--name-only", "-z", revision, "--", attachmentDir+"/").Output()
	if err != nil {
		return app.EntryPublication{}, fmt.Errorf("listing published attachments at %s: %w", revision, err)
	}
	for _, name := range strings.Split(string(files), "\x00") {
		if name != "" {
			document.Attachments = append(document.Attachments, strings.TrimPrefix(name, attachmentDir+"/"))
		}
	}
	return app.EntryPublication{Revision: revision, Document: document}, nil
}

func publishFile(ctx context.Context, root *os.Root, filename string, reader io.Reader) (err error) {
	if !fs.ValidPath(filename) || strings.HasPrefix(filename, ".sdd-runtime/") {
		return fmt.Errorf("invalid entry publication path %q", filename)
	}
	if err := root.MkdirAll(path.Dir(filename), 0o755); err != nil {
		return err
	}
	const temporary = ".sdd-runtime/publication.tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := root.Remove(temporary); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err := io.Copy(file, reader); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, filename); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(root.Name(), filepath.FromSlash(path.Dir(filename))))
}

// ReadDocument returns a graph document's current bytes, or Absent.
func (s *FilesystemGraphStore) ReadDocument(ctx context.Context, logicalPath string) (app.DocumentPublication, error) {
	if err := ctx.Err(); err != nil {
		return app.DocumentPublication{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.DocumentPublication{}, err
	}
	defer unlock(lock)
	return s.readDocumentLocked(logicalPath)
}

func (s *FilesystemGraphStore) readDocumentLocked(logicalPath string) (_ app.DocumentPublication, err error) {
	if err := validateGraphPath(s.dir, logicalPath); err != nil {
		return app.DocumentPublication{}, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return app.DocumentPublication{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	raw, err := root.ReadFile(logicalPath)
	if errors.Is(err, fs.ErrNotExist) {
		revision, err := graphDirectoryRevision(s.dir)
		return app.DocumentPublication{Revision: revision, Absent: true}, err
	}
	if err != nil {
		return app.DocumentPublication{}, err
	}
	revision, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return app.DocumentPublication{}, err
	}
	return app.DocumentPublication{Revision: revision, Content: raw}, nil
}

// LookupDocumentPublication finds what the key published for the path: on a
// Git-backed target the commit carrying the key's trailer, read at that
// revision (absent when the commit removed the document); without Git,
// nothing is ever found, so retries fall back to the document's current state.
func (s *FilesystemGraphStore) LookupDocumentPublication(ctx context.Context, key app.PublicationKey, logicalPath string) (app.DocumentPublication, bool, error) {
	if err := key.Validate(); err != nil {
		return app.DocumentPublication{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.DocumentPublication{}, false, err
	}
	defer unlock(lock)
	return s.lookupDocumentPublicationLocked(ctx, key, logicalPath)
}

func (s *FilesystemGraphStore) lookupDocumentPublicationLocked(ctx context.Context, key app.PublicationKey, logicalPath string) (app.DocumentPublication, bool, error) {
	if err := validateGraphPath(s.dir, logicalPath); err != nil {
		return app.DocumentPublication{}, false, err
	}
	if s.publicationGit == nil {
		return app.DocumentPublication{}, false, nil
	}
	revision, err := s.publicationGit.lookupTrailer(ctx, mutationTrailer(key.String()))
	if err != nil || revision == "" {
		return app.DocumentPublication{}, false, err
	}
	content, exists, err := s.publicationGit.readCommittedFile(ctx, revision, logicalPath)
	if err != nil {
		return app.DocumentPublication{}, false, err
	}
	return app.DocumentPublication{Revision: revision, Content: content, Absent: !exists}, true, nil
}

// PublishDocument writes one document change once under its key. A creation
// over a present document and a removal of an absent one change nothing; a
// replacement checks the document it replaces by blob ID. On a Git-backed
// target the publication counts as existing only after its finalizer
// committed it, so a file written or removed by an attempt whose commit was
// lost is handed to the finalizer again.
func (s *FilesystemGraphStore) PublishDocument(ctx context.Context, key app.PublicationKey, mutation app.DocumentMutation) (_ app.DocumentPublication, err error) {
	if err := key.Validate(); err != nil {
		return app.DocumentPublication{}, err
	}
	if err := validateGraphPath(s.dir, mutation.LogicalPath); err != nil {
		return app.DocumentPublication{}, err
	}
	if mutation.Content != nil && len(mutation.Content) == 0 {
		return app.DocumentPublication{}, fmt.Errorf("sdd: empty canonical bytes for %s", mutation.LogicalPath)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock()
	if err != nil {
		return app.DocumentPublication{}, err
	}
	defer unlock(lock)
	if publication, exists, err := s.lookupDocumentPublicationLocked(ctx, key, mutation.LogicalPath); err != nil || exists {
		return publication, err
	}
	current, err := s.readDocumentLocked(mutation.LogicalPath)
	if err != nil {
		return app.DocumentPublication{}, err
	}
	switch {
	case mutation.Content == nil:
		if current.Absent {
			committed, err := s.committedOnBranch(ctx, mutation.LogicalPath)
			if err != nil {
				return app.DocumentPublication{}, err
			}
			if committed {
				// Removed before a lost commit: the finalizer has a removal to commit.
				break
			}
			return app.DocumentPublication{Absent: true}, nil
		}
		root, openErr := os.OpenRoot(s.dir)
		if openErr != nil {
			return app.DocumentPublication{}, openErr
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		if err := root.Remove(mutation.LogicalPath); err != nil {
			return app.DocumentPublication{}, err
		}
		if err := syncDirectory(filepath.Join(s.dir, filepath.FromSlash(path.Dir(mutation.LogicalPath)))); err != nil {
			return app.DocumentPublication{}, err
		}
	case mutation.ExpectedBlob == "" && !current.Absent:
		// Create-if-absent: the document is present; a file whose commit was
		// lost still goes to the finalizer, anything else changes nothing.
		committed, err := s.committedOnBranch(ctx, mutation.LogicalPath)
		if err != nil {
			return app.DocumentPublication{}, err
		}
		if committed {
			return app.DocumentPublication{Content: current.Content}, nil
		}
		return app.DocumentPublication{Revision: current.Revision, Content: current.Content}, nil
	case !current.Absent && bytes.Equal(current.Content, mutation.Content):
		// Written before a lost commit; the finalizer completes it.
	case mutation.ExpectedBlob != "" && (current.Absent || app.GitBlobID(current.Content) != mutation.ExpectedBlob):
		return app.DocumentPublication{}, &app.ApplicationError{Code: app.ErrorGraphConflict, Message: "the document changed since it was read", Revision: current.Revision}
	default:
		root, openErr := os.OpenRoot(s.dir)
		if openErr != nil {
			return app.DocumentPublication{}, openErr
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		if err := publishFile(ctx, root, mutation.LogicalPath, bytes.NewReader(mutation.Content)); err != nil {
			return app.DocumentPublication{}, err
		}
	}
	revision, err := graphDirectoryRevision(s.dir)
	if err != nil {
		return app.DocumentPublication{}, err
	}
	s.recordLineage(current.Revision, revision)
	return app.DocumentPublication{Revision: revision, Content: mutation.Content, Absent: mutation.Content == nil}, nil
}
