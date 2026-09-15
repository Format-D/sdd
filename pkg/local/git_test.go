package local_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/networkteam/sdd/pkg/local"
)

type gitRepository struct {
	t    *testing.T
	root string
}

func newGitRepository(t *testing.T) *gitRepository {
	t.Helper()
	repo := &gitRepository{t: t, root: canonicalTempDir(t)}
	repo.git("init", "-b", "main")
	repo.git("config", "user.name", "Test")
	repo.git("config", "user.email", "test@example.invalid")
	repo.git("config", "commit.gpgsign", "false")
	repo.write("README", "test\n")
	repo.git("add", "README")
	repo.git("commit", "-m", "test: seed repository")
	return repo
}

func (r *gitRepository) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", append([]string{"-C", r.root}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (r *gitRepository) write(name, content string) {
	r.t.Helper()
	filename := filepath.Join(r.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepository) finalizer() local.GitFinalizer {
	return local.GitFinalizer{Checkout: r.root, GraphDir: ".sdd/graph", Branch: "main"}
}

func (r *gitRepository) graphStore() *local.FilesystemGraphStore {
	r.t.Helper()
	git := r.finalizer()
	store, err := local.NewFilesystemGraphStore(local.FilesystemGraphStoreOptions{Project: "example", GraphDir: filepath.Join(r.root, ".sdd", "graph"), Branch: "main", PublicationGit: &git})
	if err != nil {
		r.t.Fatal(err)
	}
	return store
}

func (r *gitRepository) failCommits() func() {
	r.t.Helper()
	hook := ".git/hooks/pre-commit"
	r.write(hook, "#!/bin/sh\nexit 1\n")
	filename := filepath.Join(r.root, filepath.FromSlash(hook))
	if err := os.Chmod(filename, 0o755); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		r.t.Helper()
		if err := os.Remove(filename); err != nil {
			r.t.Fatal(err)
		}
	}
}
