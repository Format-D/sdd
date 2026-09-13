package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const exampleHTTPHelperEnv = "SDD_EXAMPLE_HTTP_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(exampleHTTPHelperEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExampleHTTPTransportIsStateless(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "graph"), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), exampleHTTPHelperEnv+"=1", "SDD_EXAMPLE_DATA="+root,
		"SDD_EXAMPLE_ADDR="+addr, "SDD_EXAMPLE_TOKEN=http-test-token")
	var output bytes.Buffer
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
			if waitErr != nil {
				t.Errorf("HTTP process exited: %v\n%s", waitErr, output.String())
			}
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("HTTP process did not stop: %v\n%s", ctx.Err(), output.String())
		}
	}()

	endpoint := "http://" + addr
	probe := &http.Client{Timeout: 100 * time.Millisecond}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := probe.Do(request)
		if err == nil {
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("unauthenticated HTTP request = %d, want 401", response.StatusCode)
			}
			break
		}
		select {
		case <-done:
			t.Fatalf("HTTP process exited before serving: %v\n%s", waitErr, output.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer http-test-token")
		request.Header.Set("Mcp-Session-Id", "unrelated-transport")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
			t.Fatalf("authenticated %s = %d, Allow %q, want 405 and POST", method, response.StatusCode, response.Header.Get("Allow"))
		}
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "http-smoke", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: exampleHTTPTestTransport{}},
	}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	}()
	door, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if door.IsError {
		t.Fatalf("start_session returned a tool error: %+v", door)
	}
	content, ok := door.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("start_session content = %T", door.StructuredContent)
	}
	handle, _ := content["session"].(string)
	if handle == "" {
		t.Fatalf("start_session returned no SDD session handle: %+v", content)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "info", Arguments: map[string]any{"session": handle}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("info returned a tool error: %+v", result)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer incorrect-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("incorrect bearer token after valid requests = %d, want 401", response.StatusCode)
	}
}

type exampleHTTPTestTransport struct{}

func (exampleHTTPTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer http-test-token")
	request.Header.Set("Mcp-Session-Id", "unrelated-transport")
	response, err := http.DefaultTransport.RoundTrip(request)
	if err == nil && response.Header.Get("Mcp-Session-Id") != "" {
		return nil, errors.Join(errors.New("stateless HTTP issued a transport session ID"), response.Body.Close())
	}
	return response, err
}
