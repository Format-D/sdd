package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	app "github.com/networkteam/sdd/pkg/application"
)

const blobSuffix = ".blob"

// FilesystemStagedBlobStore keeps immutable bytes under <subject>/<session>/.
// Existing configured locations remain readable; new bytes use the first one.
type FilesystemStagedBlobStore struct {
	locations []StoreLocation
	mu        sync.Mutex
}

func NewFilesystemStagedBlobStore(locations ...StoreLocation) (*FilesystemStagedBlobStore, error) {
	if len(locations) == 0 {
		return nil, fmt.Errorf("sdd: at least one staged blob store location is required")
	}
	return &FilesystemStagedBlobStore{locations: locations}, nil
}

// NewFilesystemStagedBlobStoreAt is the single-directory form, for a
// composition with no location history to resolve across.
func NewFilesystemStagedBlobStoreAt(dir string) (*FilesystemStagedBlobStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("sdd: staged blob directory is required")
	}
	return NewFilesystemStagedBlobStore(StoreLocation{
		Name: dir, StagedBlobs: dir, Subject: "local", Project: "local",
	})
}

func (s *FilesystemStagedBlobStore) Stage(
	_ context.Context,
	ref app.SessionRef,
	filename string,
	content io.Reader,
) (app.StagedBlob, error) {
	dir, err := stagingDir(ref)
	if err != nil {
		return app.StagedBlob{}, err
	}
	if filename == "" || filepath.Base(filename) != filename || strings.ContainsAny(filename, `/\`) {
		return app.StagedBlob{}, fmt.Errorf("sdd: staged filename must be a plain name")
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return app.StagedBlob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := openStoreRoot(s.locations[0].StagedBlobs, true)
	if err != nil {
		return app.StagedBlob{}, err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return app.StagedBlob{}, err
	}
	id, err := randomBlobID()
	if err != nil {
		return app.StagedBlob{}, err
	}
	blob := app.StagedBlob{
		ID: id, Size: int64(len(data)), Filename: filename,
	}
	blobName := filepath.Join(dir, id+blobSuffix)
	if err := publishBytes(root, blobName, data); err != nil {
		return app.StagedBlob{}, err
	}

	return blob, nil
}

func (s *FilesystemStagedBlobStore) Open(_ context.Context, ref app.SessionRef, id string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, dir, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := validBlobID(id); err != nil {
		return nil, err
	}
	return root.Open(filepath.Join(dir, id+blobSuffix))
}

// DeleteStaged removes a session's resources across its known locations.
// An area that is already gone is success.
func (s *FilesystemStagedBlobStore) DeleteStaged(_ context.Context, ref app.SessionRef) error {
	dir, err := stagingDir(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, location := range s.locations {
		root, err := openStoreRoot(location.StagedBlobs, false)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		removeErr := removeStagingDir(root, dir)
		if err := errors.Join(removeErr, root.Close()); err != nil {
			return err
		}
	}
	return nil
}

// resolve opens the location holding this session's staging area, falling back
// to the first location so a caller reading nothing staged gets the ordinary
// not-exist error. The caller owns closing the returned root.
func (s *FilesystemStagedBlobStore) resolve(ref app.SessionRef) (*os.Root, string, error) {
	dir, err := stagingDir(ref)
	if err != nil {
		return nil, "", err
	}
	for _, location := range s.locations {
		root, err := openStoreRoot(location.StagedBlobs, false)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		info, statErr := root.Stat(dir)
		if statErr == nil && info.IsDir() {
			return root, dir, nil
		}
		if closeErr := root.Close(); closeErr != nil {
			return nil, "", closeErr
		}
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return nil, "", statErr
		}
	}
	root, err := openStoreRoot(s.locations[0].StagedBlobs, false)
	return root, dir, err
}

func removeStagingDir(root *os.Root, dir string) error {
	entries, err := fs.ReadDir(root.FS(), dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := root.Remove(filepath.Join(dir, entry.Name())); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := root.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncRootDir(root, filepath.Dir(dir))
}

// stagingDir is the readable path a session's staged blobs live under.
func stagingDir(ref app.SessionRef) (string, error) {
	if err := plainSegment("subject", ref.Subject); err != nil {
		return "", err
	}
	if err := plainSegment("session", string(ref.Session)); err != nil {
		return "", err
	}
	return filepath.Join(ref.Subject, string(ref.Session)), nil
}

func plainSegment(what, value string) error {
	if value == "" || value == "." || value == ".." ||
		strings.ContainsAny(value, `/\`) || filepath.Base(value) != value {
		return fmt.Errorf("sdd: blob %s must be a plain path segment, got %q", what, value)
	}
	return nil
}

func randomBlobID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validBlobID(id string) error {
	if len(id) != 32 {
		return fmt.Errorf("sdd: invalid staged blob ID")
	}
	_, err := hex.DecodeString(id)
	return err
}
