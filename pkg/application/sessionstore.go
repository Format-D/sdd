package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/networkteam/sdd/internal/engine"
	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/slogutils"
)

type SessionID string

// SessionMetadata is structured routing and ownership data. Dialogue events
// remain opaque to the store. The type itself is the metadata contract: its
// evolution is the Go type's own, and how a store survives that is the
// adapter's concern — schema migrations, or format discrimination in its
// persisted record (d-tac-8js).
type SessionMetadata struct {
	ID          SessionID
	Subject     string
	Project     ProjectID
	Participant string
	Label       string
	// Branch is the session's explicit branch binding. Empty means unbound;
	// compositions without a branch concept leave it empty.
	Branch     string `json:"branch,omitempty"`
	Attachment *Attachment
	// Ended summarizes the terminal event. Application reads derive it from
	// events; metadata cannot independently end or reopen a session.
	Ended     *SessionEnd `json:",omitempty"`
	UpdatedAt time.Time
}

// Attachment is the stamp of the client that last attached to the session and
// when the session was last acted on. It is a record, not a lock: the handle
// is the capability, integrity comes from CAS on append, and staleness is
// derived from LastActivity (d-cpt-aen).
type Attachment struct {
	Subject       string
	ClientName    string
	ClientVersion string
	LastActivity  time.Time
}

const SessionEndedEventCode = "session_ended"

// SessionEnd records the participant act that ended a session, written once and
// never revised. Reason records the abandon note, so a displaced writer's next
// call can be told why. Who ended it is the session's own participant; the
// ending client's stamp is transport and does not enter the durable record.
type SessionEnd struct {
	Act     SessionEndAct
	EndedAt time.Time
	Reason  string `json:",omitempty"`
}

// SessionEndAct is the closed set of participant acts that end a dialogue.
type SessionEndAct string

const (
	SessionConcluded SessionEndAct = "concluded"
	SessionAbandoned SessionEndAct = "abandoned"
)

// UnmarshalJSON decodes stored metadata, recovering the terminal record from the
// attachment history superseded shapes carried it in. Decoding stays lenient
// about every other field in both directions (d-cpt-i2x).
func (m *SessionMetadata) UnmarshalJSON(data []byte) error {
	type metadata SessionMetadata
	var decoded struct {
		metadata
		AttachmentHistory []legacyAttachmentRecord
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = SessionMetadata(decoded.metadata)
	if m.Ended == nil {
		m.Ended = endFromLegacyHistory(decoded.AttachmentHistory)
	}
	return nil
}

// legacyAttachmentRecord is one entry of the attachment history superseded
// shapes appended to, decoded only far enough to recover a terminal act.
type legacyAttachmentRecord struct {
	EndedAt time.Time
	Cause   string
	Reason  string
}

// endFromLegacyHistory reads a superseded history backwards for the act that
// ended the dialogue, skipping the connection events those logs also recorded —
// a dropped socket ends nothing. A takeover, or a cause this binary does not
// know, stops the scan: something came after the act, so the session is not
// ended.
func endFromLegacyHistory(history []legacyAttachmentRecord) *SessionEnd {
	for i := len(history) - 1; i >= 0; i-- {
		record := history[i]
		switch record.Cause {
		case "disconnect", "shutdown", "switch":
			continue
		case "conclude":
			return &SessionEnd{Act: SessionConcluded, EndedAt: record.EndedAt, Reason: record.Reason}
		case "abandon":
			return &SessionEnd{Act: SessionAbandoned, EndedAt: record.EndedAt, Reason: record.Reason}
		}
		return nil
	}
	return nil
}

// legacyEndStore derives ending state from events for collection, listings and
// resume. Legacy shell events remain readable; metadata is only a summary.
type legacyEndStore struct{ SessionStore }

func (s legacyEndStore) Load(ctx context.Context, id SessionID) (StoredSession, error) {
	stored, err := s.SessionStore.Load(ctx, id)
	if err != nil {
		return stored, err
	}
	if err := deriveLegacyEnd(&stored); err != nil {
		return StoredSession{}, err
	}
	return stored, nil
}

// List selects on the ending itself: the store sees no EndedBefore, because a
// legacy log's ending is derived here and the store's metadata says nothing.
// The page keeps the store's cursor, so a page thinned by this selection still
// advances the sweep.
func (s legacyEndStore) List(ctx context.Context, filter SessionFilter) (SessionPage, error) {
	inner := filter
	inner.EndedBefore = nil
	page, err := s.SessionStore.List(ctx, inner)
	if err != nil {
		return SessionPage{}, err
	}
	kept := page.Sessions[:0]
	for i := range page.Sessions {
		if err := deriveLegacyEnd(&page.Sessions[i]); err != nil {
			slogutils.FromContext(ctx).Warn("skipping unreadable session ending", "session", page.Sessions[i].Metadata.ID, "err", err)
			continue
		}
		if filter.Matches(page.Sessions[i].Metadata) {
			kept = append(kept, page.Sessions[i])
		}
	}
	page.Sessions = kept
	return page, nil
}

// deriveLegacyEnd reads an explicit ending first, then the terminal shell
// events used by older logs. Contradictory metadata never overrides either.
func deriveLegacyEnd(stored *StoredSession) error {
	for _, event := range stored.Events {
		if event.Code != SessionEndedEventCode {
			continue
		}
		if !SupportedSessionCodecVersion(event.CodecVersion) {
			return &ApplicationError{Code: ErrorMigrationRequired, Message: "unsupported session ending codec", Version: event.CodecVersion}
		}
		var end SessionEnd
		if err := json.Unmarshal(event.Payload, &end); err != nil {
			return fmt.Errorf("sdd: decode session ending for %s: %w", stored.Metadata.ID, err)
		}
		if (end.Act != SessionConcluded && end.Act != SessionAbandoned) || end.EndedAt.IsZero() {
			return fmt.Errorf("sdd: invalid session ending for %s", stored.Metadata.ID)
		}
		stored.Metadata.Ended = &end
		return nil
	}
	stored.Metadata.Ended = endFromShellEvents(stored.Events)
	return nil
}

// endFromShellEvents reads the shell instances for the act that ended the
// dialogue: logs written before the terminal record existed left the
// participant's conclude as nothing but the shell's own engine event. A shell no
// longer running is the dialogue over, since carrying it on would mean starting
// a fresh one — the revival an ended session refuses (d-tac-k4q). Both ways a
// shell leaves running map to the same act the write site records. Events this
// binary cannot decode derive nothing; the consumer reports the unreadable log.
func endFromShellEvents(events []StoredEvent) *SessionEnd {
	decoded, err := decodeWorkflowEvents(events)
	if err != nil {
		return nil
	}
	shells := map[string]bool{}
	var endedAt time.Time
	for _, event := range decoded {
		switch event.Event {
		case engine.EventStarted:
			if class, _ := event.Data["class"].(string); class == string(model.ProcedureClassShell) {
				shells[event.Instance] = false
			}
		case engine.EventCompleted, engine.EventAbandoned:
			if _, ok := shells[event.Instance]; !ok {
				continue
			}
			shells[event.Instance] = true
			if event.TS.After(endedAt) {
				endedAt = event.TS
			}
		case engine.EventMutationOutcome:
			outcome, _ := event.Data["outcome"].(string)
			returnStep, ok := event.Data["return_step"].(string)
			if _, shell := shells[event.Instance]; shell && outcome == engine.MutationCancelled && ok && returnStep == "" {
				shells[event.Instance] = true
				if event.TS.After(endedAt) {
					endedAt = event.TS
				}
			}
		}
	}
	if len(shells) == 0 {
		return nil
	}
	for _, ended := range shells {
		if !ended {
			return nil
		}
	}
	return &SessionEnd{Act: SessionConcluded, EndedAt: endedAt}
}

// StoredEvent carries the store-assigned position and append time. A zero CreatedAt
// means the historical record has no known timestamp.
type StoredEvent struct {
	Sequence     uint64
	CreatedAt    time.Time
	CodecVersion uint32
	Code         string
	Payload      json.RawMessage
}

// StoredSession.Version is the last durable event sequence, zero before the first append.
type StoredSession struct {
	Metadata SessionMetadata
	Version  uint64
	Events   []StoredEvent
}

// SessionFilter selects the sessions List returns. Subject and Project match
// metadata; EndedBefore selects sessions whose recorded ending lies before the
// instant, from metadata alone. After and Limit page the result in session-ID
// order — IDs are time-prefixed and unique, so ID order is a cursor every store
// honors; a zero Limit means every match.
type SessionFilter struct {
	Subject     string
	Project     ProjectID
	EndedBefore *time.Time
	After       SessionID
	Limit       int
}

// Matches reports whether metadata passes the filter's selection — the part a
// store applies per session, leaving After and Limit to its enumeration.
func (f SessionFilter) Matches(m SessionMetadata) bool {
	if f.Subject != "" && m.Subject != f.Subject {
		return false
	}
	if f.Project != "" && m.Project != f.Project {
		return false
	}
	if f.EndedBefore != nil && (m.Ended == nil || !m.Ended.EndedAt.Before(*f.EndedBefore)) {
		return false
	}
	return true
}

// SessionPage is one List result. Next is the cursor to continue from, empty
// once the store is exhausted. A page may hold fewer sessions than Limit, or
// none, while Next is still set: the store stopped at Limit before the filter
// admitted enough, so a consumer loops until Next is empty rather than reading
// exhaustion off the page length.
type SessionPage struct {
	Sessions []StoredSession
	Next     SessionID
}

type SessionAppend struct {
	Metadata *SessionMetadata
	Events   []StoredEvent
}

// SessionStore persists structured metadata plus ordered opaque events. Append
// requires events and atomically compares the last event sequence. It assigns event
// sequences and timestamps and updates informational metadata in the same write.
//
// Compositions must not run mixed engine versions against one session store:
// metadata carries no version guard (d-tac-8js), so an older engine reading
// metadata a newer one wrote is undetected there — only a session the newer
// engine actually advanced fails closed, through StoredEvent.CodecVersion.
//
// List is also the enumeration collection sweeps over, paged by the filter's
// cursor so a sweep converges over repeated calls instead of loading every
// session, and Delete is what makes sweeps possible against any implementation
// rather than only the local one. Delete must be idempotent: removing a session
// that is already gone is success, since two sweeps may derive the same target
// set.
type SessionStore interface {
	Create(context.Context, SessionMetadata) (StoredSession, error)
	Load(context.Context, SessionID) (StoredSession, error)
	List(context.Context, SessionFilter) (SessionPage, error)
	Append(context.Context, SessionID, uint64, SessionAppend) (uint64, error)
	Delete(context.Context, SessionID) error
}
