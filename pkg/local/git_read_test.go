package local_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
	"github.com/networkteam/sdd/pkg/local"
)

func TestGitWorktreeReadAcquisitionWithoutMutationFactory(t *testing.T) {
	repo := newGitRepository(t)
	root := repo.root
	git := repo.git
	work := filepath.Join(t.TempDir(), "work")
	git("worktree", "add", "-b", "work", work)
	acquirer, err := local.NewGitWorktreeAcquirer(local.GitWorktreeAcquirerOptions{Project: "example", ServerCheckout: root,
		ReadFactory: func(ctx context.Context, checkout string, q sdd.SnapshotReadQuery) (*sdd.AcquiredSnapshot, error) {
			selected, _ := filepath.EvalSymlinks(checkout)
			want := root
			if q.Branch == "work" {
				want = work
			}
			want, _ = filepath.EvalSymlinks(want)
			if selected != want {
				t.Fatalf("checkout=%q want %q", selected, want)
			}
			graph, err := local.NewFilesystemGraphStore(local.FilesystemGraphStoreOptions{Project: "example", GraphDir: filepath.Join(checkout, ".sdd", "graph"), Branch: q.Branch})
			if err != nil {
				return nil, err
			}
			return graph.AcquireSnapshot(ctx, q)
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"", "main", "work"} {
		source, err := acquirer.AcquireSnapshot(t.Context(), sdd.SnapshotReadQuery{Branch: branch})
		if err != nil {
			t.Fatal(err)
		}
		if source.Snapshot.Project() != "example" {
			t.Fatal("wrong project")
		}
		if err := source.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := acquirer.AcquireSnapshot(t.Context(), sdd.SnapshotReadQuery{Branch: "missing"}); err == nil {
		t.Fatal("unregistered branch accepted")
	}
	if _, err := acquirer.Acquire(t.Context(), sdd.MutationTarget{Project: "example", Branch: "work"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("mutation acquisition=%v", err)
	}
}
