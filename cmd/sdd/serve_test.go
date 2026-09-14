package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdd "github.com/networkteam/sdd/pkg/application"
	localadapter "github.com/networkteam/sdd/pkg/local"
)

const (
	stdioServeHelperEnv = "SDD_STDIO_SERVE_HELPER"
	// mainHelperArgsEnv carries a space-separated sdd argv for a subprocess
	// that runs the real CLI (e.g. the production-path test seeds the index
	// via `sdd index`). The subprocess re-enters main() with these args.
	mainHelperArgsEnv = "SDD_MAIN_HELPER_ARGS"
)

func TestMain(m *testing.M) {
	if args := os.Getenv(mainHelperArgsEnv); args != "" {
		os.Args = append([]string{"sdd"}, strings.Fields(args)...)
		main()
		os.Exit(0)
	}
	if os.Getenv(stdioServeHelperEnv) == "1" {
		os.Args = []string{"sdd", "--graph-dir", ".sdd/graph", "serve", "--transport", "stdio"}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLocalGitFinalizerCommitsBatchOnce(t *testing.T) {
	checkout, runGit := newLocalGitCheckout(t)
	graphDir := filepath.Join(checkout, ".sdd", "graph")
	entryPath := filepath.Join(graphDir, "2026/07/13-120000-s-tac-api.md")
	attachmentPath := filepath.Join(graphDir, "2026/07/13-120000-s-tac-api", "evidence.md")
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachmentPath, []byte("evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finalizer := localadapter.GitFinalizer{Checkout: checkout, GraphDir: ".sdd/graph", Branch: "main"}
	mutation := sdd.AppliedMutation{
		BatchID: "mutation-1",
		Batch: sdd.MutationBatch{
			Message: "sdd: signal tactical captured",
			Changes: []sdd.DocumentChange{{LogicalPath: "2026/07/13-120000-s-tac-api.md"}},
			Attachments: []sdd.AttachmentMaterialization{
				{LogicalPath: "2026/07/13-120000-s-tac-api/evidence.md"},
				{LogicalPath: "2026/07/13-120000-s-tac-api/evidence.md"},
			},
		},
	}
	if err := finalizer.Finalize(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Finalize(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	if count := runGit("log", "--fixed-strings", "--grep=SDD-Mutation: mutation-1", "--format=%H"); len(strings.Fields(count)) != 1 {
		t.Fatalf("matching commits = %q", count)
	}
}

func TestLocalMutationTargetFinalizesPublishedCapture(t *testing.T) {
	for _, tt := range []struct {
		name   string
		legacy bool
	}{
		{name: "new publication"},
		{name: "legacy publication", legacy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkout, runGit := newLocalGitCheckout(t)
			key := sdd.PublicationKey{Session: "session", Sequence: 4, Discriminator: "new-entry"}
			const entryID = "20260914-020000-s-tac-cap"
			batch := localCaptureBatch(t, key, entryID)
			if tt.legacy {
				change := batch.Changes[0]
				entryPath := filepath.Join(checkout, ".sdd", "graph", filepath.FromSlash(change.LogicalPath))
				if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(entryPath, change.CanonicalBytes, 0o644); err != nil {
					t.Fatal(err)
				}
				runGit("add", "--", entryPath)
				runGit("commit", "-m", "sdd: previous capture\n\nSDD-Mutation: session/4/new-entry", "--", entryPath)
			}

			first, found, err := completeLocalCapture(t, checkout, key, entryID, batch)
			if err != nil || found != tt.legacy {
				t.Fatalf("first attempt: found=%v, err=%v", found, err)
			}
			retried, found, err := completeLocalCapture(t, checkout, key, entryID, batch)
			if err != nil || !found || retried.Revision != first.Revision {
				t.Fatalf("reacquired retry = %+v, found=%v, err=%v", retried, found, err)
			}
			if tt.legacy {
				otherIntent := key
				otherIntent.Sequence++
				batch.ID = otherIntent.String()
				if _, _, err := completeLocalCapture(t, checkout, key, entryID, batch); err == nil {
					t.Fatal("legacy publication satisfied a different intent's finalizer")
				}
			}
			if runGit("rev-parse", "HEAD") != first.Revision {
				t.Fatal("finalization did not retain the original publication commit")
			}
			if count := runGit("rev-list", "--count", "HEAD"); count != "2" {
				t.Fatalf("commit count = %s; want seed and publication only", count)
			}
		})
	}
}

func localCaptureBatch(t *testing.T, key sdd.PublicationKey, entryID string) sdd.MutationBatch {
	t.Helper()
	logicalPath, err := sdd.EntryRelPath(entryID)
	if err != nil {
		t.Fatal(err)
	}
	canonical := []byte("---\ntype: signal\nkind: gap\nlayer: tactical\nconfidence: high\nparticipants: [Christopher]\nsummary: Local capture is committed once.\n---\n\nCapture through the local MCP mutation target.\n")
	document, err := sdd.ParseEntryDocument(logicalPath, canonical)
	if err != nil {
		t.Fatal(err)
	}
	return sdd.MutationBatch{
		ID: key.String(), Message: "sdd: capture " + entryID,
		Changes: []sdd.DocumentChange{{LogicalPath: logicalPath, CanonicalBytes: canonical, Document: &document}},
	}
}

func completeLocalCapture(t *testing.T, checkout string, key sdd.PublicationKey, entryID string, batch sdd.MutationBatch) (sdd.EntryPublication, bool, error) {
	t.Helper()
	targets, err := newLocalMutationTargets("example", checkout)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := targets.Acquire(t.Context(), sdd.MutationTarget{Project: "example", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := acquired.Release(); err != nil {
			t.Error(err)
		}
	}()
	publisher, ok := acquired.Graph.(sdd.EntryPublicationStore)
	if !ok || len(acquired.Finalizers) != 1 {
		t.Fatal("local target must supply entry publication and its Git finalizer")
	}
	publication, found, err := publisher.LookupEntryPublication(t.Context(), key, entryID)
	if err != nil {
		return publication, found, err
	}
	if !found {
		publication, err = publisher.PublishEntry(t.Context(), key, batch, nil)
		if err != nil {
			return publication, found, err
		}
	}
	mutation := sdd.AppliedMutation{Project: "example", BatchID: batch.ID, Revision: publication.Revision, Batch: batch}
	for _, finalizer := range acquired.Finalizers {
		if err := finalizer.Finalize(t.Context(), mutation); err != nil {
			return publication, found, err
		}
	}
	return publication, found, nil
}

func newLocalGitCheckout(t *testing.T) (string, func(...string) string) {
	t.Helper()
	checkout := canonicalTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(checkout, "xdg-config"))
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", checkout}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.invalid")
	runGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, ".sdd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".sdd", "config.yaml"), []byte("repo_id: example\ngraph_dir: .sdd/graph\ndefault_branch: main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md", ".sdd/config.yaml")
	runGit("commit", "-m", "fixture")
	return checkout, runGit
}

func TestLocalHTTPBearerAuth(t *testing.T) {
	server := httptest.NewServer(localBearerAuth("secret-token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	defer server.Close()

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request should 401, got %d", response.StatusCode)
	}
}

func TestServeStdioTransport(t *testing.T) {
	root := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".sdd", "graph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sdd", "config.yaml"), []byte("graph_dir: .sdd/graph\ndefault_branch: main\nrepo_id: example.test/stdio-smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sdd", "config.local.yaml"), []byte("participant: Stdio Smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		stdioServeHelperEnv+"=1",
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "xdg-cache"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-smoke", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to stdio server: %v\nstderr:\n%s", err, stderr.String())
	}
	door, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{}})
	if err != nil || door.IsError {
		_ = session.Close()
		t.Fatalf("start_session over stdio: %v %+v\nstderr:\n%s", err, door, stderr.String())
	}
	handle, _ := door.StructuredContent.(map[string]any)["session"].(string)
	if handle == "" {
		_ = session.Close()
		t.Fatalf("start_session served no session handle: %+v", door.StructuredContent)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "info", Arguments: map[string]any{"session": handle}})
	if err != nil {
		_ = session.Close()
		t.Fatalf("call info over stdio: %v\nstderr:\n%s", err, stderr.String())
	}
	if result.IsError {
		_ = session.Close()
		t.Fatalf("info returned a tool error: %+v\nstderr:\n%s", result, stderr.String())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close stdio session: %v\nstderr:\n%s", err, stderr.String())
	}
}
