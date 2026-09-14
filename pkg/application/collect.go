package application

import (
	"context"
	"time"

	"github.com/networkteam/slogutils"
)

// CollectSessionsCmd asks for one page of ended-session reclamation. Retention
// is how long an ended session is kept; zero removes it after ending. Limit zero
// means every match. After continues from a previous result's Next cursor.
type CollectSessionsCmd struct {
	Retention time.Duration
	Limit     int
	After     SessionID
}

// CollectSessionsResult reports removed resources and unreadable sessions. Next
// is the session cursor to continue from, empty when enumeration is complete.
type CollectSessionsResult struct {
	RemovedSessions []SessionID
	RemovedStaged   []SessionRef
	Skipped         []SessionID
	Next            SessionID
}

// CollectSessions removes resources before deleting their ended session. A
// failed resource deletion leaves the session available for the next pass.
// Collection never executes pending user writes. Ended sessions cannot resume,
// while open sessions remain regardless of inactivity or pending work.
func (a *Application) CollectSessions(ctx context.Context, cmd CollectSessionsCmd) (CollectSessionsResult, error) {
	endedBefore := a.now().UTC().Add(-cmd.Retention)
	page, err := a.sessions.List(ctx, SessionFilter{EndedBefore: &endedBefore, After: cmd.After, Limit: cmd.Limit})
	if err != nil {
		return CollectSessionsResult{}, err
	}
	result := CollectSessionsResult{}
	for _, session := range page.Sessions {
		id := session.Metadata.ID
		if err := validateStoredSession(session); err != nil {
			slogutils.FromContext(ctx).Warn("skipping unreadable session", "session", id, "err", err)
			result.Skipped = append(result.Skipped, id)
			continue
		}
		if end := session.Metadata.Ended; end == nil || !end.EndedAt.Before(endedBefore) {
			continue
		}
		ref := SessionRef{Subject: session.Metadata.Subject, Session: id}
		if err := a.blobs.DeleteStaged(ctx, ref); err != nil {
			return result, err
		}
		result.RemovedStaged = append(result.RemovedStaged, ref)
		if err := a.sessions.Delete(ctx, id); err != nil {
			return result, err
		}
		result.RemovedSessions = append(result.RemovedSessions, id)
	}
	result.Next = page.Next
	return result, nil
}
