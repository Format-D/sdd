package local_test

import (
	"strings"
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
	"github.com/networkteam/sdd/pkg/local"
)

func TestGitFinalizerCommitsBatchOnce(t *testing.T) {
	repo := newGitRepository(t)
	const entryPath = "2026/07/13-120000-s-tac-api.md"
	const attachmentPath = "2026/07/13-120000-s-tac-api/evidence.md"
	repo.write(".sdd/graph/"+entryPath, "entry\n")
	repo.write(".sdd/graph/"+attachmentPath, "evidence\n")
	finalizer := local.GitFinalizer{Checkout: repo.root, GraphDir: ".sdd/graph", Branch: "main"}
	mutation := sdd.AppliedMutation{
		BatchID: "mutation-1",
		Batch: sdd.MutationBatch{
			Message: "sdd: signal tactical captured",
			Changes: []sdd.DocumentChange{{LogicalPath: entryPath}},
			Attachments: []sdd.AttachmentMaterialization{
				{LogicalPath: attachmentPath},
				{LogicalPath: attachmentPath},
			},
		},
	}
	for range 2 {
		if err := finalizer.Finalize(t.Context(), mutation); err != nil {
			t.Fatal(err)
		}
	}
	if commits := repo.git("log", "--fixed-strings", "--grep=SDD-Mutation: mutation-1", "--format=%H"); len(strings.Fields(commits)) != 1 {
		t.Fatalf("matching commits = %q", commits)
	}
}
