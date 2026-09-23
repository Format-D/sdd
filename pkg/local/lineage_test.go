package local_test

import (
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
	localadapter "github.com/networkteam/sdd/pkg/local"
)

// A read that must include an earlier write is answered from the lineage every
// store over the graph directory shares: a fresh store, which never saw the
// writes, still shows the current revision descends from the first one, and
// still refuses a revision nobody published.
func TestIncludesRevisionIsAnsweredByAFreshStore(t *testing.T) {
	dir := canonicalTempDir(t)
	open := func() *localadapter.FilesystemGraphStore {
		store, err := localadapter.NewFilesystemGraphStore(localadapter.FilesystemGraphStoreOptions{Project: "example", GraphDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	writer := open()
	content := []byte("---\nentry: 20260923-010000-s-tac-sum\nparticipant: Christopher\n---\n\nWork.\n")
	first, err := writer.PublishDocument(t.Context(), sdd.PublicationKey{Session: "s1", Sequence: 2, Discriminator: "wipStart:a"}, sdd.DocumentMutation{LogicalPath: "wip/20260923-010000-a.md", Content: content, Message: "a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := writer.PublishDocument(t.Context(), sdd.PublicationKey{Session: "s1", Sequence: 4, Discriminator: "wipStart:b"}, sdd.DocumentMutation{LogicalPath: "wip/20260923-010100-b.md", Content: content, Message: "b"})
	if err != nil || second.Revision == first.Revision {
		t.Fatalf("second publication = %+v, %v; want a new revision", second, err)
	}

	reader := open()
	acquired, err := reader.AcquireSnapshot(t.Context(), sdd.SnapshotReadQuery{IncludesRevision: first.Revision})
	if err != nil {
		t.Fatalf("a fresh store must show the current revision includes the first write: %v", err)
	}
	if acquired.Snapshot.Revision() != second.Revision {
		t.Fatalf("snapshot revision = %s, want the current %s", acquired.Snapshot.Revision(), second.Revision)
	}
	if err := acquired.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.AcquireSnapshot(t.Context(), sdd.SnapshotReadQuery{IncludesRevision: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}); err == nil {
		t.Fatal("a revision nobody published must not be shown as included")
	}
}
