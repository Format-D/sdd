package cliapp_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/networkteam/sdd/internal/index"
)

// fakeOllama is an Ollama-compatible embedding server (POST /api/embed). It
// returns deterministic vectors per input text, stable across servers so a
// warm store never needs re-embedding, and records every input it saw.
type fakeOllama struct {
	mu     sync.Mutex
	inputs []string
	calls  int
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/embed" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls++
	f.inputs = append(f.inputs, req.Input...)
	f.mu.Unlock()
	embeddings := make([][]float32, len(req.Input))
	for i, text := range req.Input {
		sum := sha256.Sum256([]byte(text))
		vector := make([]float32, 8)
		for j := range vector {
			vector[j] = float32(binary.BigEndian.Uint32(sum[j*4:j*4+4]))/float32(^uint32(0)) + 0.01
		}
		embeddings[i] = vector
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings, "prompt_eval_count": len(req.Input)})
}

func (f *fakeOllama) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs, f.calls = nil, 0
}

func (f *fakeOllama) snapshot() (calls int, inputs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]string(nil), f.inputs...)
}

// TestServeReusesPersistentIndexAcrossRestarts guards the production path for
// s-tac-ex4: a fresh `sdd serve` answers phrase search from the machine-global
// index the CLI built, without re-embedding graph documents. Every server is a
// fresh command tree over the same cache root; counters and store identity are
// the evidence, never elapsed time.
func TestServeReusesPersistentIndexAcrossRestarts(t *testing.T) {
	ollama := &fakeOllama{}
	server := httptest.NewServer(ollama)
	defer server.Close()
	f := newAppFixture(t)
	f.write(t, ".sdd/config.yaml", "repo_id: example.test/index\ngraph_dir: .sdd/graph\ndefault_branch: main\n"+
		"embedding:\n  provider: ollama\n  model: fake-model\n  ollama_endpoint: "+server.URL+"\n")
	f.writeEntry(t, "20260101-100000-s-tac-aaa", "Alpha orchard notes", "The alpha entry is about apple orchards.")
	f.writeEntry(t, "20260101-100001-s-tac-bbb", "Beta cultivation notes", "The beta entry is about beta cultivation.")

	if result := f.run(t, "index"); result.err != nil {
		t.Fatalf("sdd index: %v\n%s", result.err, result.stderr)
	}
	if calls, _ := ollama.snapshot(); calls == 0 {
		t.Fatal("index made no embedding calls, nothing was seeded")
	}
	storeDir := f.singleIndexStore(t)
	seeded := manifestEntryCount(t, storeDir)
	if seeded < 2 {
		t.Fatalf("seeded manifest has %d entries, want at least the two graph entries", seeded)
	}

	ollama.reset()
	if hits := f.serveSearch(t); !strings.Contains(hits, "s-tac-aaa") && !strings.Contains(hits, "s-tac-bbb") {
		t.Fatalf("first server surfaced no seeded entry: %q", hits)
	}
	assertWarm(t, ollama, "first server")
	ollama.reset()
	f.serveSearch(t)
	assertWarm(t, ollama, "second server")

	f.writeEntry(t, "20260101-100002-s-tac-ccc", "Zeta topic notes", "The zeta entry mentions zeta only.")
	ollama.reset()
	f.serveSearch(t)
	calls, inputs := ollama.snapshot()
	var documents, queries int
	for _, input := range inputs {
		if input == "alpha" {
			queries++
			continue
		}
		documents++
		if !strings.Contains(strings.ToLower(input), "zeta") {
			t.Errorf("third server embedded a document that is not the new entry: %q", input)
		}
	}
	if queries != 1 || documents == 0 || calls < 2 {
		t.Errorf("third server: %d calls, %d query embeds, %d document embeds; want one query and only the new entry", calls, queries, documents)
	}
	if got := manifestEntryCount(t, storeDir); got != seeded+1 {
		t.Errorf("store grew to %d entries, want %d", got, seeded+1)
	}

	ollama.reset()
	f.serveSearch(t)
	assertWarm(t, ollama, "fourth server")
	f.singleIndexStore(t)
}

// assertWarm asserts a server made exactly one embedding call carrying one
// input, the query, and embedded no document.
func assertWarm(t *testing.T, ollama *fakeOllama, label string) {
	t.Helper()
	calls, inputs := ollama.snapshot()
	if calls != 1 || len(inputs) != 1 {
		t.Errorf("%s: %d embed calls with inputs %v; want one call with the query only", label, calls, inputs)
	}
}

func (f *appFixture) writeEntry(t *testing.T, id, summary, body string) {
	t.Helper()
	name := filepath.ToSlash(filepath.Join(".sdd", "graph", id[:4], id[4:6], id[6:]+".md"))
	f.write(t, name, "---\ntype: signal\nkind: gap\nlayer: tactical\nconfidence: high\nparticipants:\n  - Local Author\nsummary: "+summary+"\n---\n\n"+body+"\n")
}

// serveSearch starts a fresh server, runs one vector search over MCP and
// returns the structured result as JSON text.
func (f *appFixture) serveSearch(t *testing.T) string {
	const phrase = "alpha"
	t.Helper()
	var encoded []byte
	f.serve(t, func(client *mcp.ClientSession) {
		door, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{}})
		if err != nil || door.IsError {
			t.Fatalf("start_session: %v %+v", err, door)
		}
		handle, _ := door.StructuredContent.(map[string]any)["session"].(string)
		if handle == "" {
			t.Fatalf("start_session served no session handle: %+v", door.StructuredContent)
		}
		// Fake embeddings are content-hash noise, so ranking is arbitrary; a
		// limit covering the whole corpus keeps the assertion independent of
		// base content edits.
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"session": handle, "query": phrase, "limit": 50}})
		if err != nil || result.IsError {
			t.Fatalf("search: %v %+v", err, result)
		}
		if encoded, err = json.Marshal(result.StructuredContent); err != nil {
			t.Fatal(err)
		}
	})
	return string(encoded)
}

// singleIndexStore returns the one machine-global store directory under the
// fixture's cache root and fails when the production path created another.
func (f *appFixture) singleIndexStore(t *testing.T) string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(f.root, "cache"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "manifest.json" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil || len(dirs) != 1 {
		t.Fatalf("machine-global index stores = %v, %v; want exactly one", dirs, err)
	}
	return dirs[0]
}

func manifestEntryCount(t *testing.T, storeDir string) int {
	t.Helper()
	manifest, err := index.LoadManifest(storeDir)
	if err != nil {
		t.Fatalf("load manifest at %s: %v", storeDir, err)
	}
	return len(manifest.Entries)
}
