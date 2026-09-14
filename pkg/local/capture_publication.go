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
	if s.publicationGit != nil {
		revision, err := s.publicationGit.lookupRevision(ctx, key.String())
		if err != nil || revision == "" {
			return app.EntryPublication{}, false, err
		}
		publication, err := s.readCommittedEntry(ctx, revision, logicalPath)
		return publication, err == nil, err
	}
	return s.readExistingEntry(ctx, logicalPath)
}

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
		if err := publishFile(ctx, root, change.LogicalPath, bytes.NewReader(change.CanonicalBytes)); err != nil {
			return app.EntryPublication{}, err
		}
		publication, _, err = s.readExistingEntry(ctx, change.LogicalPath)
		if err != nil {
			return app.EntryPublication{}, err
		}
	}
	if s.publicationGit == nil {
		return publication, nil
	}
	actual := app.MutationBatch{ID: key.String(), Message: batch.Message, Changes: []app.DocumentChange{{LogicalPath: publication.Document.LogicalPath, Document: &publication.Document}}}
	attachmentDir, err := model.AttachDirRelPath(entryID)
	if err != nil {
		return app.EntryPublication{}, err
	}
	for _, filename := range publication.Document.Attachments {
		actual.Attachments = append(actual.Attachments, app.AttachmentMaterialization{SourceName: filename, LogicalPath: path.Join(attachmentDir, filename)})
	}
	if err := s.publicationGit.finalizeLocked(ctx, app.AppliedMutation{Project: s.project, BatchID: key.String(), Batch: actual}); err != nil {
		return app.EntryPublication{}, err
	}
	revision, err := s.publicationGit.lookupRevision(ctx, key.String())
	if err != nil {
		return app.EntryPublication{}, err
	}
	return s.readCommittedEntry(ctx, revision, change.LogicalPath)
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
