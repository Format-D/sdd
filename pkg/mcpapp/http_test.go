package mcpapp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testHTTPServer struct {
	server  *httptest.Server
	mu      sync.RWMutex
	handler http.Handler
}

func newTestHTTPServer(t *testing.T, handler http.Handler, verifier auth.TokenVerifier) *testHTTPServer {
	t.Helper()
	if verifier == nil {
		verifier = func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
			if token != "tester" && token != "other" {
				return nil, auth.ErrInvalidToken
			}
			return &auth.TokenInfo{UserID: token, Expiration: time.Now().Add(time.Hour)}, nil
		}
	}
	endpoint := &testHTTPServer{handler: handler}
	endpoint.server = httptest.NewServer(auth.RequireBearerToken(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.mu.RLock()
		current := endpoint.handler
		endpoint.mu.RUnlock()
		current.ServeHTTP(w, r)
	})))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (s *testHTTPServer) SetHandler(handler http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = handler
}

func (s *testHTTPServer) Transport(token string) *testHTTPTransport {
	transport := &testHTTPTransport{base: s.server.Client().Transport, headers: make(http.Header)}
	transport.StreamableClientTransport = &mcp.StreamableClientTransport{
		Endpoint:   s.server.URL,
		HTTPClient: &http.Client{Transport: transport},
	}
	transport.SetToken(token)
	return transport
}

type testHTTPResponse struct {
	StatusCode int
	Header     http.Header
	Body       string
}

func (s *testHTTPServer) Request(t *testing.T, method, token string, headers http.Header, body string) testHTTPResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, s.server.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := s.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return testHTTPResponse{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: string(data)}
}

type testHTTPTransport struct {
	*mcp.StreamableClientTransport
	base            http.RoundTripper
	mu              sync.RWMutex
	headers         http.Header
	responseHeaders []http.Header
}

func (t *testHTTPTransport) SetToken(token string) {
	value := ""
	if token != "" {
		value = "Bearer " + token
	}
	t.SetHeader("Authorization", value)
}

func (t *testHTTPTransport) SetHeader(name, value string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if value == "" {
		t.headers.Del(name)
	} else {
		t.headers.Set(name, value)
	}
}

func (t *testHTTPTransport) ResponseHeaders() []http.Header {
	t.mu.RLock()
	defer t.mu.RUnlock()
	headers := make([]http.Header, len(t.responseHeaders))
	for i, header := range t.responseHeaders {
		headers[i] = header.Clone()
	}
	return headers
}

func (t *testHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	t.mu.RLock()
	for name, values := range t.headers {
		clone.Header[name] = append([]string(nil), values...)
	}
	t.mu.RUnlock()
	response, err := t.base.RoundTrip(clone)
	if err == nil {
		t.mu.Lock()
		t.responseHeaders = append(t.responseHeaders, response.Header.Clone())
		t.mu.Unlock()
	}
	return response, err
}
