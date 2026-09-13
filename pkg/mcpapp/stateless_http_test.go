package mcpapp_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdd "github.com/networkteam/sdd/pkg/application"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

func useHTTPIdentity(opts *testServerOptions) { opts.MCP.LocalIdentity = sdd.RequestIdentity{} }

func TestStatelessHTTPMethods(t *testing.T) {
	env := newTestServer(t, nil, "", "", useHTTPIdentity)
	endpoint := newTestHTTPServer(t, env.srv.Handler(), nil)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := endpoint.Request(t, method, "tester", nil, "")
			if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
				t.Fatalf("%s = %d, Allow %q; want 405, POST", method, response.StatusCode, response.Header.Get("Allow"))
			}
		})
	}
}

func TestStatelessHTTPIgnoresTransportSessionHeader(t *testing.T) {
	for _, header := range []string{"", "unrelated-transport"} {
		t.Run("session-header="+header, func(t *testing.T) {
			env := newTestServer(t, nil, "", "", useHTTPIdentity)
			endpoint := newTestHTTPServer(t, env.srv.Handler(), nil)
			transport := endpoint.Transport("tester")
			transport.SetHeader("Mcp-Session-Id", header)
			client := connect(t, env.srv, transport)
			if client.ID() != "" {
				t.Fatalf("HTTP issued transport session ID %q", client.ID())
			}
			door := openSession(t, client)
			var info mcpserver.InfoResult
			call(t, client, "info", map[string]any{"session": door.Session}, &info)
			if info.Project != "test" {
				t.Fatalf("info project = %q, want test", info.Project)
			}
			for _, response := range transport.ResponseHeaders() {
				if id := response.Get("Mcp-Session-Id"); id != "" {
					t.Fatalf("HTTP response issued transport session ID %q", id)
				}
			}
		})
	}
}

func TestStatelessHTTPAlternatesWarmReplicas(t *testing.T) {
	first := newTestServer(t, nil, "", "", useHTTPIdentity)
	second := newTestServer(t, nil, first.graphDir, first.sessionsDir, useHTTPIdentity)
	endpoint := newTestHTTPServer(t, first.srv.Handler(), nil)
	client := connect(t, first.srv, endpoint.Transport("tester"))
	door := openSession(t, client)

	endpoint.SetHandler(second.srv.Handler())
	var capture mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &capture)
	requireFullReportSchema(t, "first capture", capture.ReportSchema)
	endpoint.SetHandler(first.srv.Handler())
	call(t, client, "next", map[string]any{"session": door.Session, "instance": capture.Instance, "report": assembleReport()}, &capture)
	if capture.Step != "playback" {
		t.Fatalf("first replica's step = %q, want playback", capture.Step)
	}
	endpoint.SetHandler(second.srv.Handler())
	call(t, client, "next", map[string]any{"session": door.Session, "instance": capture.Instance, "report": map[string]any{
		"chooser": "playback", "choice": "adjust", "fields": map[string]any{"body": "Revised across replicas."},
	}}, &capture)
	if !strings.Contains(capture.Instructions, "Revised across replicas.") {
		t.Fatalf("second replica did not load the chooser: %+v", capture)
	}

	endpoint.SetHandler(first.srv.Handler())
	var another mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &another)
	requireStubReportSchema(t, "schema served on other replica", another.ReportSchema)
}

func TestStatelessHTTPContinuesAfterServerRestart(t *testing.T) {
	first := newTestServer(t, nil, "", "", useHTTPIdentity)
	endpoint := newTestHTTPServer(t, first.srv.Handler(), nil)
	client := connect(t, first.srv, endpoint.Transport("tester"))
	door := openSession(t, client)
	var capture mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &capture)
	if err := first.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	restarted := newTestServer(t, nil, first.graphDir, first.sessionsDir, useHTTPIdentity)
	endpoint.SetHandler(restarted.srv.Handler())
	call(t, client, "next", map[string]any{"session": door.Session, "instance": capture.Instance, "report": assembleReport()}, &capture)
	if capture.Step != "playback" {
		t.Fatalf("restarted replica's step = %q, want playback", capture.Step)
	}
}

func TestStatelessHTTPFreshReplicaPreservesMetadata(t *testing.T) {
	first := newTestServer(t, nil, "", "", useHTTPIdentity)
	endpoint := newTestHTTPServer(t, first.srv.Handler(), nil)
	client := connect(t, first.srv, endpoint.Transport("tester"))
	door := openSession(t, client)
	stored, err := first.sessions.Load(t.Context(), sdd.SessionID(door.Session))
	if err != nil {
		t.Fatal(err)
	}

	second := newTestServer(t, nil, first.graphDir, first.sessionsDir, useHTTPIdentity)
	endpoint.SetHandler(second.srv.Handler())
	var info mcpserver.InfoResult
	call(t, client, "info", map[string]any{"session": door.Session}, &info)
	after, err := first.sessions.Load(t.Context(), stored.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, after) {
		t.Fatal("fresh-replica read changed the stored session")
	}
}

func TestStatelessHTTPResumeReorientsServedContent(t *testing.T) {
	first := newTestServer(t, nil, "", "", useHTTPIdentity)
	endpoint := newTestHTTPServer(t, first.srv.Handler(), nil)
	client := connect(t, first.srv, endpoint.Transport("tester"))
	door := openSession(t, client)
	var capture mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &capture)

	second := newTestServer(t, nil, first.graphDir, first.sessionsDir, useHTTPIdentity)
	endpoint.SetHandler(second.srv.Handler())
	var another mcpserver.ServeResult
	call(t, client, "start_procedure", map[string]any{"session": door.Session, "canonical": "capture"}, &another)
	requireStubReportSchema(t, "already served capture", another.ReportSchema)
	var resumed mcpserver.ResumeSessionResult
	call(t, client, "resume_session", map[string]any{"session": door.Session}, &resumed)
	if len(resumed.Open) != 3 {
		t.Fatalf("resume returned %d instances, want shell and two captures", len(resumed.Open))
	}
	for _, open := range resumed.Open {
		if open.Instance == capture.Instance {
			requireFullReportSchema(t, "resumed capture", open.ReportSchema)
			return
		}
	}
	t.Fatalf("resume lost capture %s", capture.Instance)
}

func TestStatelessHTTPWarmReplicaRefusesEndedSession(t *testing.T) {
	first := newTestServer(t, nil, "", "", useHTTPIdentity)
	second := newTestServer(t, nil, first.graphDir, first.sessionsDir, useHTTPIdentity)
	endpoint := newTestHTTPServer(t, first.srv.Handler(), nil)
	client := connect(t, first.srv, endpoint.Transport("tester"))
	door := openSession(t, client)
	endpoint.SetHandler(second.srv.Handler())
	var result mcpserver.AbandonResult
	call(t, client, "abandon", map[string]any{"session": door.Session, "reason": "test complete"}, &result)

	endpoint.SetHandler(first.srv.Handler())
	message := callExpectError(t, client, "info", map[string]any{"session": door.Session})
	if !strings.Contains(message, "ended") && !strings.Contains(message, "torn down") {
		t.Fatalf("ended-session refusal = %q", message)
	}
}

func TestStatelessHTTPRefusesRequestsWhileClosing(t *testing.T) {
	env := newTestServer(t, nil, "", "", useHTTPIdentity)
	endpoint := newTestHTTPServer(t, env.srv.Handler(), nil)
	if err := env.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "unrelated-transport"} {
		t.Run("session-header="+header, func(t *testing.T) {
			response := endpoint.Request(t, http.MethodPost, "tester", http.Header{"Mcp-Session-Id": []string{header}}, "")
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("closing server returned %d, want 503", response.StatusCode)
			}
		})
	}
}

func TestSessionReplayErrorStopsTool(t *testing.T) {
	for _, tc := range []struct {
		name string
		warm bool
	}{{name: "cold"}, {name: "warm", warm: true}} {
		t.Run(tc.name, func(t *testing.T) {
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
			if !tc.warm {
				replica := newTestServer(t, nil, env.graphDir, env.sessionsDir)
				client = connect(t, replica.srv)
			}
			for range 2 {
				message := callExpectError(t, client, "info", map[string]any{"session": door.Session})
				if !strings.Contains(message, "decoding workflow event") {
					t.Fatalf("replay error = %q", message)
				}
			}
			after, err := env.sessions.Load(t.Context(), stored.Metadata.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != stored.Version+1 {
				t.Fatal("failed replay changed the stored session")
			}
		})
	}
}

func TestSessionRequestLoadsLedgerOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		warm bool
	}{{name: "cold"}, {name: "warm", warm: true}} {
		t.Run(tc.name, func(t *testing.T) {
			first := newTestServer(t, nil, "", "")
			door := openSession(t, connect(t, first.srv))
			store := &sessionStoreProbe{SessionStore: first.sessions}
			replica := newTestServer(t, nil, first.graphDir, first.sessionsDir, func(opts *testServerOptions) { opts.Application.Sessions = store })
			client := connect(t, replica.srv)
			var result mcpserver.RegistryResult
			if tc.warm {
				call(t, client, "registry", map[string]any{"session": door.Session}, &result)
			}
			before := store.loads.Load()
			call(t, client, "registry", map[string]any{"session": door.Session}, &result)
			if got := store.loads.Load() - before; got != 1 {
				t.Fatalf("request loaded ledger %d times, want 1", got)
			}
			if len(result.Functions) == 0 {
				t.Fatal("registry returned no functions")
			}
		})
	}
}

func TestConcurrentColdSessionRequestsRefreshAfterCacheWinner(t *testing.T) {
	first := newTestServer(t, nil, "", "")
	door := openSession(t, connect(t, first.srv))
	store := &sessionStoreProbe{SessionStore: first.sessions}
	replica := newTestServer(t, nil, first.graphDir, first.sessionsDir, func(opts *testServerOptions) { opts.Application.Sessions = store })
	client := connect(t, replica.srv)
	store.PauseLoads(2)
	args := map[string]any{"session": door.Session, "canonical": "capture"}
	finishA := callAsync[mcpserver.ServeResult](t, client, "start_procedure", args)
	loadA := store.NextLoad(t)
	finishB := callAsync[mcpserver.ServeResult](t, client, "start_procedure", args)
	loadB := store.NextLoad(t)
	if loadA.Stored.Version != loadB.Stored.Version {
		t.Fatal("requests did not read the same version")
	}

	loadA.Resume()
	a := finishA()
	loadB.Resume()
	b := finishB()
	if a.Instance == "" || b.Instance == "" || a.Instance == b.Instance {
		t.Fatalf("instance handles = %q and %q", a.Instance, b.Instance)
	}
	var resumed mcpserver.ResumeSessionResult
	call(t, client, "resume_session", map[string]any{"session": door.Session}, &resumed)
	for _, id := range []string{a.Instance, b.Instance} {
		if !slices.ContainsFunc(resumed.Open, func(open mcpserver.ServeResult) bool { return open.Instance == id }) {
			t.Fatalf("resume lost instance %s", id)
		}
	}
}
