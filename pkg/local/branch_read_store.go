package local

import (
	"context"
	"fmt"

	app "github.com/networkteam/sdd/pkg/application"
)

// BranchReadStore is the local read source of a served checkout. A read of a
// named branch goes through that branch's registered checkout; an unbound
// read, the session's derived base, reads the serving checkout's own graph.
// When the serving checkout is detached the base is the default branch, read
// through its checkout (20260923-233057-d-cpt-ekd); with no checkout of it
// either, the unbound read stays on the serving checkout so the session still
// opens and its framing can say so, while every unbound write fails.
type BranchReadStore struct {
	// GraphStore is the serving checkout's graph; it must support snapshot reads.
	app.GraphStore
	Branches      *GitWorktreeAcquirer
	DefaultBranch string
}

func (s BranchReadStore) AcquireSnapshot(ctx context.Context, q app.SnapshotReadQuery) (*app.AcquiredSnapshot, error) {
	if q.Branch == "" {
		base, err := s.Branches.BaseBranch(ctx)
		if err != nil {
			return nil, err
		}
		if base == "" && s.Branches.ValidateBranch(ctx, app.MutationTarget{Project: s.Branches.Project(), Branch: s.DefaultBranch}) == nil {
			q.Branch = s.DefaultBranch
		}
	}
	if q.Branch != "" {
		return s.Branches.AcquireSnapshot(ctx, q)
	}
	reader, ok := s.GraphStore.(app.SnapshotReader)
	if !ok {
		return nil, fmt.Errorf("sdd: the serving checkout's graph store does not support snapshot reads")
	}
	return reader.AcquireSnapshot(ctx, q)
}
