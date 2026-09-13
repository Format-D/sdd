package mcpapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdd "github.com/networkteam/sdd/pkg/application"
	pkgllm "github.com/networkteam/sdd/pkg/llm"
	localadapter "github.com/networkteam/sdd/pkg/local"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

type statelessRoundTripper func(*http.Request) (*http.Response, error)

func (f statelessRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type httpReplica struct{ handler http.Handler }

func TestStatelessHTTPAlternatesWarmReplicasAndRestarts(t *testing.T) {
	options := func(opts *mcpserver.Options) {
		opts.LocalIdentity = sdd.RequestIdentity{}
	}
	first := newTestServer(t, nil, "", "", options)
	second := newTestServer(t, nil, first.graphDir, first.sessionsDir, options)
	a := &httpReplica{handler: first.srv.Handler()}
	b := &httpReplica{handler: second.srv.Handler()}
	var active atomic.Pointer[httpReplica]
	active.Store(a)
	verifier := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if token != "tester" && token != "other" {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: token, Expiration: time.Now().Add(time.Hour)}, nil
	}
	endpoint := httptest.NewServer(auth.RequireBearerToken(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Load().handler.ServeHTTP(w, r)
	})))
	defer endpoint.Close()
	var token atomic.Value
	token.Store("tester")
	var bogusHeader atomic.Bool
	client := mcp.NewClient(&mcp.Implementation{Name: "stateless-test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint.URL,
		HTTPClient: &http.Client{Transport: statelessRoundTripper(func(req *http.Request) (*http.Response, error) {
			request := req.Clone(req.Context())
			request.Header = req.Header.Clone()
			request.Header.Set("Authorization", "Bearer "+token.Load().(string))
			if bogusHeader.Load() {
				request.Header.Set("Mcp-Session-Id", "unrelated-transport")
			}
			response, err := http.DefaultTransport.RoundTrip(request)
			if err == nil && response.Header.Get("Mcp-Session-Id") != "" {
				return nil, errors.Join(errors.New("stateless response issued a transport session ID"), response.Body.Close())
			}
			return response, err
		})},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("closing MCP client session: %v", err)
		}
	}()

	door := openSession(t, session)
	stored, err := first.sessions.Load(t.Context(), sdd.SessionID(door.Session))
	if err != nil {
		t.Fatal(err)
	}
	active.Store(b)
	var info mcpserver.InfoResult
	call(t, session, "info", map[string]any{"session": door.Session}, &info)
	afterRead, err := first.sessions.Load(t.Context(), stored.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, afterRead) {
		t.Fatal("a read on a fresh replica changed the stored session")
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request, err := http.NewRequestWithContext(t.Context(), method, endpoint.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer tester")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s returned %d, want 405", method, response.StatusCode)
		}
	}
	bogusHeader.Store(true)
	var capture mcpserver.ServeResult
	call(t, session, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &capture)
	requireFullReportSchema(t, "first capture", capture.ReportSchema)
	active.Store(a)
	bogusHeader.Store(false)
	call(t, session, "next", map[string]any{"session": door.Session, "instance": capture.Instance, "report": assembleReport()}, &capture)
	if capture.Step != "playback" {
		t.Fatalf("A did not load B's instance: %+v", capture)
	}
	active.Store(b)
	call(t, session, "next", map[string]any{"session": door.Session, "instance": capture.Instance, "report": map[string]any{
		"chooser": "playback", "choice": "adjust", "fields": map[string]any{"body": "Revised across replicas."},
	}}, &capture)
	if !strings.Contains(capture.Instructions, "Revised across replicas.") {
		t.Fatalf("B did not load A's chooser: %+v", capture)
	}
	active.Store(a)
	var another mcpserver.ServeResult
	call(t, session, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &another)
	requireStubReportSchema(t, "capture served on other replica", another.ReportSchema)

	restarted := newTestServer(t, nil, first.graphDir, first.sessionsDir, options)
	active.Store(&httpReplica{handler: restarted.srv.Handler()})
	call(t, session, "next", map[string]any{"session": door.Session, "instance": another.Instance, "report": assembleReport()}, &another)
	if another.Step != "playback" {
		t.Fatalf("restarted replica lost the session: %+v", another)
	}

	active.Store(b)
	token.Store("other")
	if message := callExpectError(t, session, "info", map[string]any{"session": door.Session}); !strings.Contains(message, "owner") && !strings.Contains(message, "belong") {
		t.Fatalf("cached session authorization error = %q", message)
	}
	token.Store("tester")
	var resumed mcpserver.ResumeSessionResult
	call(t, session, "resume_session", map[string]any{"session": door.Session}, &resumed)
	if len(resumed.Open) != 3 {
		t.Fatalf("resume lost instances: %+v", resumed.Open)
	}
	var full bool
	for _, open := range resumed.Open {
		if open.Procedure == "capture" {
			if _, stub := open.ReportSchema["served_earlier"]; !stub {
				full = true
			}
		}
	}
	if !full {
		t.Fatal("explicit resume did not reset served memory")
	}

	var abandoned mcpserver.AbandonResult
	call(t, session, "abandon", map[string]any{"session": door.Session, "reason": "test complete"}, &abandoned)
	active.Store(a)
	if message := callExpectError(t, session, "info", map[string]any{"session": door.Session}); !strings.Contains(message, "ended") && !strings.Contains(message, "torn down") {
		t.Fatalf("warmed replica continued ended session: %q", message)
	}

	if err := first.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "unrelated-transport"} {
		request := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", nil)
		request.Header.Set("Mcp-Session-Id", header)
		response := httptest.NewRecorder()
		a.handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("shutdown with header %q = %d", header, response.Code)
		}
	}
}

func TestWarmSessionReplayErrorStopsTool(t *testing.T) {
	env := newTestServer(t, nil, "", "")
	client := connect(t, env.srv)
	door := openSession(t, client)
	stored, err := env.sessions.Load(t.Context(), sdd.SessionID(door.Session))
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.sessions.Append(t.Context(), stored.Metadata.ID, stored.Version, sdd.SessionAppend{Events: []sdd.StoredEvent{{
		CodecVersion: sdd.SessionCodecVersion, Code: sdd.WorkflowEventCode, Payload: json.RawMessage(`"invalid workflow event"`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		message := callExpectError(t, client, "info", map[string]any{"session": door.Session})
		if !strings.Contains(message, "decoding workflow event") {
			t.Fatalf("warm replay failure = %q", message)
		}
	}
	after, err := env.sessions.Load(t.Context(), stored.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != stored.Version+1 {
		t.Fatal("replay failure allowed a tool or attachment stamp to persist")
	}
}

type countingSessionStore struct {
	sdd.SessionStore
	loads atomic.Int64
}

func (s *countingSessionStore) Load(ctx context.Context, id sdd.SessionID) (sdd.StoredSession, error) {
	s.loads.Add(1)
	return s.SessionStore.Load(ctx, id)
}

func newServerWithSessionStore(t *testing.T, graphDir string, sessions sdd.SessionStore) *mcpserver.Server {
	t.Helper()
	graph, err := localadapter.NewFilesystemGraphStore(localadapter.FilesystemGraphStoreOptions{
		Project: "test", GraphDir: graphDir, Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := sdd.NewProjectRuntime(sdd.ProjectRuntimeOptions{
		Project: sdd.ProjectRef{ID: "test"}, Graph: graph, DefaultBranch: "main",
		LLM: pkgllm.RunnerFunc(func(context.Context, pkgllm.Request) (pkgllm.Result, error) {
			return pkgllm.Result{}, errors.New("unexpected LLM request")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := localadapter.NewFilesystemStagedBlobStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app, err := sdd.NewApplication(sdd.ApplicationOptions{
		Access: rootAccess{runtime: runtime}, Sessions: sessions, StagedBlobs: blobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpserver.New(mcpserver.Options{
		Application: app, LocalIdentity: sdd.RequestIdentity{Subject: "tester"}, SearchSyncMode: sdd.SearchSyncNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestSessionRequestLoadsLedgerOnceOnColdAndWarmCache(t *testing.T) {
	first := newTestServer(t, nil, "", "")
	door := openSession(t, connect(t, first.srv))
	sessions := &countingSessionStore{SessionStore: first.sessions}
	server := newServerWithSessionStore(t, first.graphDir, sessions)
	client := connect(t, server)
	for _, state := range []string{"cold", "warm"} {
		t.Run(state, func(t *testing.T) {
			sessions.loads.Store(0)
			var result mcpserver.RegistryResult
			call(t, client, "registry", map[string]any{"session": door.Session}, &result)
			if len(result.Functions) == 0 {
				t.Fatal("registry returned no functions")
			}
			if got := sessions.loads.Load(); got != 1 {
				t.Fatalf("request loaded the session ledger %d times, want 1", got)
			}
		})
	}
}

// coldLoadBarrier returns each of the first two loads only when the test lets
// that request proceed. Both requests can therefore replay the same old ledger.
type coldLoadBarrier struct {
	sdd.SessionStore
	loads  atomic.Int64
	loaded chan blockedSessionLoad
}

type blockedSessionLoad struct {
	stored  sdd.StoredSession
	release chan struct{}
}

func (s *coldLoadBarrier) Load(ctx context.Context, id sdd.SessionID) (sdd.StoredSession, error) {
	stored, err := s.SessionStore.Load(ctx, id)
	if err != nil || s.loads.Add(1) > 2 {
		return stored, err
	}
	load := blockedSessionLoad{stored: stored, release: make(chan struct{})}
	select {
	case s.loaded <- load:
	case <-ctx.Done():
		return sdd.StoredSession{}, ctx.Err()
	}
	select {
	case <-load.release:
		return stored, nil
	case <-ctx.Done():
		return sdd.StoredSession{}, ctx.Err()
	}
}

func TestConcurrentColdSessionRequestsRefreshAfterCacheWinner(t *testing.T) {
	first := newTestServer(t, nil, "", "")
	door := openSession(t, connect(t, first.srv))
	sessions := &coldLoadBarrier{SessionStore: first.sessions, loaded: make(chan blockedSessionLoad, 2)}
	server := newServerWithSessionStore(t, first.graphDir, sessions)
	client := connect(t, server)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	type reply struct {
		result *mcp.CallToolResult
		err    error
	}
	start := func() <-chan reply {
		done := make(chan reply, 1)
		go func() {
			result, err := client.CallTool(ctx, &mcp.CallToolParams{
				Name: "start_procedure", Arguments: map[string]any{"session": door.Session, "canonical": "capture"},
			})
			done <- reply{result: result, err: err}
		}()
		return done
	}
	waitLoad := func() blockedSessionLoad {
		t.Helper()
		select {
		case load := <-sessions.loaded:
			return load
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return blockedSessionLoad{}
		}
	}
	waitReply := func(done <-chan reply) mcpserver.ServeResult {
		t.Helper()
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.result.IsError {
				t.Fatalf("start_procedure returned tool error: %s", contentText(got.result))
			}
			var result mcpserver.ServeResult
			decodeStructured(t, got.result, &result)
			return result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return mcpserver.ServeResult{}
		}
	}

	firstReply := start()
	firstLoad := waitLoad()
	secondReply := start()
	secondLoad := waitLoad()
	if firstLoad.stored.Version != secondLoad.stored.Version {
		t.Fatal("cold requests did not read the same session version")
	}
	close(firstLoad.release)
	a := waitReply(firstReply)
	// The second request carries the old ledger, but must discard it after the
	// first request wins the cache entry and persists its procedure instance.
	close(secondLoad.release)
	b := waitReply(secondReply)
	if a.Instance == "" || b.Instance == "" || a.Instance == b.Instance {
		t.Fatalf("concurrent requests returned instance handles %q and %q", a.Instance, b.Instance)
	}

	var resumed mcpserver.ResumeSessionResult
	call(t, client, "resume_session", map[string]any{"session": door.Session}, &resumed)
	found := map[string]bool{}
	for _, open := range resumed.Open {
		found[open.Instance] = true
	}
	if !found[a.Instance] || !found[b.Instance] {
		t.Fatalf("resume lost a concurrently started instance: %+v", resumed.Open)
	}
}
