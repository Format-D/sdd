package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	sdd "github.com/networkteam/sdd/pkg/application"
)

// The stores below are what an external composition owning its own storage has
// to write. They are here rather than in a test file because conforming to the
// ports is the thing this example demonstrates, and sddtest holds them to the
// same behaviour the local adapters meet.

// memorySessionStore keeps sessions in a map. Append is the only mutation and
// compares the expected version under the lock, which is the whole of the
// concurrency contract.
type memorySessionStore struct {
	mu       sync.Mutex
	sessions map[sdd.SessionID]sdd.StoredSession
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{sessions: map[sdd.SessionID]sdd.StoredSession{}}
}

func (s *memorySessionStore) Create(_ context.Context, metadata sdd.SessionMetadata) (sdd.StoredSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[metadata.ID]; exists {
		return sdd.StoredSession{}, &sdd.ApplicationError{
			Code: sdd.ErrorSessionConflict, Message: "session already exists",
		}
	}
	stored := sdd.StoredSession{Metadata: metadata}
	s.sessions[metadata.ID] = cloneSession(stored)
	return stored, nil
}

func (s *memorySessionStore) Load(_ context.Context, id sdd.SessionID) (sdd.StoredSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.sessions[id]
	if !exists {
		return sdd.StoredSession{}, fmt.Errorf("%w: %s", sdd.ErrSessionNotFound, id)
	}
	return cloneSession(stored), nil
}

// List pages in ID order from the cursor: the page holds the matches among the
// IDs it walked, and Next names the last ID walked when Limit stopped it.
func (s *memorySessionStore) List(_ context.Context, filter sdd.SessionFilter) (sdd.SessionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]sdd.SessionID, 0, len(s.sessions))
	for id := range s.sessions {
		if filter.After == "" || id > filter.After {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, cmpID)
	page := sdd.SessionPage{}
	for i, id := range ids {
		if filter.Limit > 0 && len(page.Sessions) >= filter.Limit {
			page.Next = ids[i-1]
			break
		}
		if stored := s.sessions[id]; filter.Matches(stored.Metadata) {
			page.Sessions = append(page.Sessions, cloneSession(stored))
		}
	}
	return page, nil
}

func (s *memorySessionStore) Append(
	_ context.Context,
	id sdd.SessionID,
	expectedVersion uint64,
	add sdd.SessionAppend,
) (uint64, error) {
	if len(add.Events) == 0 {
		return 0, &sdd.ApplicationError{Code: sdd.ErrorInvalidArgument, Message: "session append requires events"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.sessions[id]
	if !exists {
		return 0, fmt.Errorf("%w: %s", sdd.ErrSessionNotFound, id)
	}
	if stored.Version != expectedVersion {
		return stored.Version, &sdd.ApplicationError{
			Code: sdd.ErrorSessionConflict, Message: "session version changed",
		}
	}
	if add.Metadata != nil {
		if add.Metadata.ID != id || add.Metadata.Subject != stored.Metadata.Subject ||
			add.Metadata.Project != stored.Metadata.Project {
			return stored.Version, &sdd.ApplicationError{
				Code: sdd.ErrorSessionOwnership, Message: "session identity and project are immutable",
			}
		}
		stored.Metadata = *add.Metadata
	}
	events := slices.Clone(add.Events)
	now := time.Now().UTC()
	for i := range events {
		events[i].Sequence = stored.Version + uint64(i) + 1
		events[i].CreatedAt = now
	}
	stored.Events = append(slices.Clip(stored.Events), events...)
	stored.Version += uint64(len(events))
	s.sessions[id] = cloneSession(stored)
	return stored.Version, nil
}

// Delete is idempotent: two sweeps derive the same target set, so removing what
// is already gone is success.
func (s *memorySessionStore) Delete(_ context.Context, id sdd.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

// cloneSession keeps callers from mutating stored events or metadata.
func cloneSession(stored sdd.StoredSession) sdd.StoredSession {
	stored.Events = slices.Clone(stored.Events)
	for i := range stored.Events {
		stored.Events[i].Payload = bytes.Clone(stored.Events[i].Payload)
	}
	if stored.Metadata.Attachment != nil {
		attachment := *stored.Metadata.Attachment
		stored.Metadata.Attachment = &attachment
	}
	if stored.Metadata.Ended != nil {
		ended := *stored.Metadata.Ended
		stored.Metadata.Ended = &ended
	}
	return stored
}

func cmpID(a, b sdd.SessionID) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// memoryStagedBlobStore keeps immutable bytes scoped to their session.
type memoryStagedBlobStore struct {
	mu     sync.Mutex
	areas  map[sdd.SessionRef]map[string][]byte
	staged int
}

func newMemoryStagedBlobStore() *memoryStagedBlobStore {
	return &memoryStagedBlobStore{areas: map[sdd.SessionRef]map[string][]byte{}}
}

func (s *memoryStagedBlobStore) Stage(
	_ context.Context,
	session sdd.SessionRef,
	filename string,
	reader io.Reader,
) (sdd.StagedBlob, error) {
	content, err := io.ReadAll(reader)
	if err != nil {
		return sdd.StagedBlob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Identity is per stage, not per content: staging the same bytes twice is
	// two blobs, and each is addressed on its own.
	s.staged++
	blob := sdd.StagedBlob{
		ID:       fmt.Sprintf("blob-%d", s.staged),
		Size:     int64(len(content)),
		Filename: filename,
	}
	area, exists := s.areas[session]
	if !exists {
		area = map[string][]byte{}
		s.areas[session] = area
	}
	area[blob.ID] = content
	return blob, nil
}

func (s *memoryStagedBlobStore) Open(_ context.Context, session sdd.SessionRef, id string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.lookup(session, id)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

// DeleteStaged removes every blob belonging to the session.
func (s *memoryStagedBlobStore) DeleteStaged(_ context.Context, session sdd.SessionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.areas, session)
	return nil
}

func (s *memoryStagedBlobStore) lookup(session sdd.SessionRef, id string) ([]byte, error) {
	area, exists := s.areas[session]
	if !exists {
		return nil, fmt.Errorf("no staging area for session %s", session.Session)
	}
	content, exists := area[id]
	if !exists {
		return nil, fmt.Errorf("staged blob %s not found", id)
	}
	return content, nil
}
