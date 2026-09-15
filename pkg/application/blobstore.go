package application

import (
	"context"
	"io"
)

// SessionRef addresses one session inside a subject's namespace. Staged blobs
// are scoped to a session, so this is what names their area — there is no owner
// entity, just the two fields that identify whose scaffolding this is.
type SessionRef struct {
	Subject string
	Session SessionID
}

type StagedBlob struct {
	ID       string
	Size     int64
	Filename string
}

// StagedBlobStore owns immutable scratch bytes scoped to a session. Stage's
// metadata describes the upload response; filename references belong in session
// events. DeleteStaged removes every resource before its session is deleted and
// must succeed when that session's resources are already gone.
type StagedBlobStore interface {
	Stage(context.Context, SessionRef, string, io.Reader) (StagedBlob, error)
	Open(context.Context, SessionRef, string) (io.ReadCloser, error)
	DeleteStaged(context.Context, SessionRef) error
}
