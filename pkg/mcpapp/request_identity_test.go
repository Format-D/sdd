package mcpapp_test

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	sdd "github.com/networkteam/sdd/pkg/application"
	mcpserver "github.com/networkteam/sdd/pkg/mcpapp"
)

func TestHTTPWorkflowUsesCurrentRequestIdentity(t *testing.T) {
	access := &observingAccess{}
	env := newTestServer(t, nil, "", "", func(opts *testServerOptions) {
		access.rootAccess = opts.Application.Access.(rootAccess)
		opts.Application.Access = access
		opts.MCP.LocalIdentity = sdd.RequestIdentity{}
	})
	identities := map[string]sdd.RequestIdentity{
		"read":  {Subject: "tester", Scopes: []string{"project:read"}, Attributes: map[string]any{"sentinel": "read-request"}},
		"write": {Subject: "tester", Scopes: []string{"project:read", "project:write"}, Attributes: map[string]any{"sentinel": "write-request"}},
		"other": {Subject: "other", Scopes: []string{"project:read", "project:write"}},
	}
	endpoint := newTestHTTPServer(t, env.srv.Handler(), func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		identity, ok := identities[token]
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: identity.Subject, Scopes: identity.Scopes, Extra: identity.Attributes, Expiration: time.Now().Add(time.Hour)}, nil
	})
	transport := endpoint.Transport("read")
	client := connect(t, env.srv, transport)
	opened := openSession(t, client)
	access.requireIdentity(t, identities["read"])

	var shown mcpserver.ShowResult
	call(t, client, "show", map[string]any{"session": opened.Session, "ids": []string{fixtureGapID}}, &shown)
	if !strings.Contains(shown.Entries, "oscillation") {
		t.Fatalf("fixture entry was not served: %q", shown.Entries)
	}
	access.requireIdentity(t, identities["read"])

	transport.SetToken("write")
	entryID := runCaptureToCompletion(t, client, opened.Session, "Current HTTP write authority")
	access.requireIdentity(t, identities["write"])
	call(t, client, "show", map[string]any{"session": opened.Session, "ids": []string{entryID}}, &shown)
	if !strings.Contains(shown.Entries, entryID) {
		t.Fatalf("created entry %q was not served: %q", entryID, shown.Entries)
	}
	access.requireIdentity(t, identities["write"])

	transport.SetToken("other")
	message := callExpectError(t, client, "info", map[string]any{"session": opened.Session})
	if !strings.Contains(message, "owner") && !strings.Contains(message, "belong") {
		t.Fatalf("session ownership refusal = %q", message)
	}
	access.requireIdentity(t, identities["other"])
}

type observingAccess struct {
	rootAccess
	mu            sync.Mutex
	seen          []sdd.RequestIdentity
	currentScopes map[string][]string
}

func (a *observingAccess) ResolvePrincipal(_ context.Context, identity sdd.RequestIdentity) (sdd.Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, identity)
	if a.currentScopes == nil {
		a.currentScopes = make(map[string][]string)
	}
	a.currentScopes[identity.Subject] = slices.Clone(identity.Scopes)
	return sdd.Principal{Subject: identity.Subject}, nil
}

func (a *observingAccess) ResolveProject(ctx context.Context, principal sdd.Principal, project sdd.ProjectID, access sdd.Access) (*sdd.ProjectRuntime, error) {
	a.mu.Lock()
	scopes := slices.Clone(a.currentScopes[principal.Subject])
	a.mu.Unlock()
	want, code := "project:read", sdd.ErrorReadDenied
	if access == sdd.AccessWrite {
		want, code = "project:write", sdd.ErrorWriteDenied
	}
	if !slices.Contains(scopes, want) {
		return nil, &sdd.ApplicationError{Code: code, Message: "scope denied"}
	}
	return a.rootAccess.ResolveProject(ctx, principal, project, access)
}

func (a *observingAccess) requireIdentity(t *testing.T, want sdd.RequestIdentity) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.seen) == 0 {
		t.Fatal("request did not resolve its identity")
	}
	for _, got := range a.seen {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request identity = %+v, want %+v", got, want)
		}
	}
	a.seen = nil
}
