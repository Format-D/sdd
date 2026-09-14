package local_test

import (
	"path/filepath"
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
	"github.com/networkteam/sdd/pkg/local"
)

func TestRepositoryTargetFinalizesPublishedCapture(t *testing.T) {
	for _, tt := range []struct {
		name   string
		legacy bool
	}{
		{name: "new publication"},
		{name: "legacy publication", legacy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newGitRepository(t)
			repo.write(".sdd/config.yaml", "repo_id: example\ngraph_dir: .sdd/graph\ndefault_branch: main\n")
			key := sdd.PublicationKey{Session: "session", Sequence: 4, Discriminator: "new-entry"}
			const entryID = "20260914-020000-s-tac-cap"
			batch := captureBatch(t, key, entryID, "Local capture is committed once.")
			if tt.legacy {
				change := batch.Changes[0]
				entryPath := ".sdd/graph/" + change.LogicalPath
				repo.write(entryPath, string(change.CanonicalBytes))
				repo.git("add", "--", entryPath)
				repo.git("commit", "-m", "sdd: previous capture\n\nSDD-Mutation: session/4/new-entry", "--", entryPath)
			}

			first, found, err := repo.completeCapture(key, entryID, batch)
			if err != nil || found != tt.legacy {
				t.Fatalf("first attempt: found=%v, err=%v", found, err)
			}
			retried, found, err := repo.completeCapture(key, entryID, batch)
			if err != nil || !found || retried.Revision != first.Revision {
				t.Fatalf("reacquired retry = %+v, found=%v, err=%v", retried, found, err)
			}
			if tt.legacy {
				otherIntent := key
				otherIntent.Sequence++
				batch.ID = otherIntent.String()
				if _, _, err := repo.completeCapture(key, entryID, batch); err == nil {
					t.Fatal("legacy publication satisfied a different intent's finalizer")
				}
			}
			if repo.git("rev-parse", "HEAD") != first.Revision {
				t.Fatal("finalization did not retain the original publication commit")
			}
			if count := repo.git("rev-list", "--count", "HEAD"); count != "2" {
				t.Fatalf("commit count = %s; want seed and publication only", count)
			}
		})
	}
}

func TestRepositoryTargetsReloadConfiguration(t *testing.T) {
	repo := newGitRepository(t)
	repo.write(".sdd/config.yaml", "repo_id: committed\ndefault_branch: main\n")
	targets := repo.targets("example")
	for _, tt := range []struct {
		name         string
		localID      string
		globalConfig string
		wantErr      bool
	}{
		{name: "local overlay", localID: "example"},
		{name: "changed local identity", localID: "other", wantErr: true},
		{name: "changed global config", localID: "example", globalConfig: "participant: [", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo.write(".sdd/config.local.yaml", "repo_id: "+tt.localID+"\n")
			repo.write("global-config.yaml", tt.globalConfig)
			read, err := targets.AcquireSnapshot(t.Context(), sdd.SnapshotReadQuery{Branch: "main"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("read acquisition error = %v, want error = %v", err, tt.wantErr)
			}
			if read != nil {
				if err := read.Release(); err != nil {
					t.Fatal(err)
				}
			}
			write, err := targets.Acquire(t.Context(), sdd.MutationTarget{Project: "example", Branch: "main"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("write acquisition error = %v, want error = %v", err, tt.wantErr)
			}
			if write != nil {
				if err := write.Release(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func (r *gitRepository) targets(project sdd.ProjectID) *local.GitWorktreeAcquirer {
	r.t.Helper()
	targets, err := local.NewRepositoryTargets(project, r.root, filepath.Join(r.root, "global-config.yaml"))
	if err != nil {
		r.t.Fatal(err)
	}
	return targets
}

func (r *gitRepository) completeCapture(key sdd.PublicationKey, entryID string, batch sdd.MutationBatch) (sdd.EntryPublication, bool, error) {
	r.t.Helper()
	acquired, err := r.targets("example").Acquire(r.t.Context(), sdd.MutationTarget{Project: "example", Branch: "main"})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() {
		if err := acquired.Release(); err != nil {
			r.t.Error(err)
		}
	}()
	publisher, ok := acquired.Graph.(sdd.EntryPublicationStore)
	if !ok || len(acquired.Finalizers) != 1 {
		r.t.Fatal("local target must supply entry publication and its Git finalizer")
	}
	publication, found, err := publisher.LookupEntryPublication(r.t.Context(), key, entryID)
	if err != nil {
		return publication, found, err
	}
	if !found {
		publication, err = publisher.PublishEntry(r.t.Context(), key, batch, nil)
		if err != nil {
			return publication, found, err
		}
	}
	mutation := sdd.AppliedMutation{Project: "example", BatchID: batch.ID, Revision: publication.Revision, Batch: batch}
	for _, finalizer := range acquired.Finalizers {
		if err := finalizer.Finalize(r.t.Context(), mutation); err != nil {
			return publication, found, err
		}
	}
	return publication, found, nil
}
