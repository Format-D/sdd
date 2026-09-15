package mcpapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/networkteam/sdd/internal/engine"
	sdd "github.com/networkteam/sdd/pkg/application"
)

type sessionStoreProbe struct {
	sdd.SessionStore
	loads       atomic.Int64
	mu          sync.Mutex
	remaining   int
	paused      chan *pausedSessionLoad
	rejectEvent engine.EventType
}

type pausedSessionLoad struct {
	Stored  sdd.StoredSession
	release chan struct{}
}

func (p *pausedSessionLoad) Resume() { close(p.release) }

func (s *sessionStoreProbe) Load(ctx context.Context, id sdd.SessionID) (sdd.StoredSession, error) {
	s.loads.Add(1)
	stored, err := s.SessionStore.Load(ctx, id)
	if err != nil {
		return stored, err
	}
	s.mu.Lock()
	var paused chan *pausedSessionLoad
	if s.remaining > 0 {
		s.remaining--
		paused = s.paused
	}
	s.mu.Unlock()
	if paused != nil {
		load := &pausedSessionLoad{Stored: stored, release: make(chan struct{})}
		paused <- load
		select {
		case <-load.release:
		case <-ctx.Done():
			return sdd.StoredSession{}, ctx.Err()
		}
	}
	return stored, nil
}

// PauseLoads stops the next n successful loads after reading their stored version.
func (s *sessionStoreProbe) PauseLoads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remaining = n
	s.paused = make(chan *pausedSessionLoad, n)
}

func (s *sessionStoreProbe) NextLoad(t *testing.T) *pausedSessionLoad {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case load := <-s.paused:
		return load
	case <-ctx.Done():
		t.Fatalf("waiting for a paused session load: %v", ctx.Err())
		return nil
	}
}

func (s *sessionStoreProbe) FailNextEvent(kind engine.EventType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectEvent = kind
}

func (s *sessionStoreProbe) Append(ctx context.Context, id sdd.SessionID, version uint64, data sdd.SessionAppend) (uint64, error) {
	s.mu.Lock()
	for _, stored := range data.Events {
		if s.rejectEvent == "" || stored.Code != sdd.WorkflowEventCode {
			continue
		}
		var event engine.Event
		if err := json.Unmarshal(stored.Payload, &event); err != nil {
			s.mu.Unlock()
			return version, err
		}
		if event.Event == s.rejectEvent {
			s.rejectEvent = ""
			s.mu.Unlock()
			return version, errors.New("session event append unavailable")
		}
	}
	s.mu.Unlock()
	return s.SessionStore.Append(ctx, id, version, data)
}
