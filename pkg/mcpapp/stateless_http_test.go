package mcpapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdd "github.com/networkteam/sdd/pkg/application"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

type statelessRoundTripper func(*http.Request) (*http.Response, error)

func (f statelessRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type httpReplica struct{ handler http.Handler }

func TestStatelessHTTPAlternatesWarmReplicasAndRestarts(t *testing.T) {
	options := func(opts *mcpserver.Options) {
		opts.StatelessHTTP = true
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
		Endpoint:             endpoint.URL,
		DisableStandaloneSSE: true,
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
	active.Store(b)
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
