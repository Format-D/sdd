package gollm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/pkg/llm"

	gollmrunner "github.com/networkteam/sdd/internal/llm/gollm"
)

// What actually reaches the provider is not inferable from the adapter code —
// gollm copies its option map into the request body verbatim, so a key we set
// may land under a name the provider ignores. This drives a real request at a
// local listener and asserts on the bytes.
func TestOllamaRequestWire(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"model":"m","response":"{}","done":true,"prompt_eval_count":11,"eval_count":22}`))
	}))
	defer srv.Close()

	runner, err := gollmrunner.NewRunner(model.LLMConfig{
		Provider: "ollama", Model: "m", OllamaEndpoint: srv.URL,
		Params: map[string]string{"think": "high"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), llm.Request{
		SystemPrompt: "SYSTEM-BLOCK-MARKER",
		UserPrompt:   "USER-BLOCK-MARKER",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("request keys: %v", keysOf(body))
	t.Logf("full body: %+v", body)

	if body["think"] != "high" {
		t.Errorf("think = %v, want high", body["think"])
	}
	prompt, _ := body["prompt"].(string)
	if !strings.Contains(prompt, "SYSTEM-BLOCK-MARKER") {
		t.Errorf("system block missing from the flattened prompt: %q", prompt)
	}
	// Ollama flattens through Prompt.String(), which used to render the input
	// both bare and again under a Messages header — every call paid twice.
	if n := strings.Count(prompt, "USER-BLOCK-MARKER"); n != 1 {
		t.Errorf("user block sent %d times, want 1: %q", n, prompt)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 22 {
		t.Errorf("usage not parsed: %+v", res.Usage)
	}
	if res.Identity.Variant != "think=high" {
		t.Errorf("variant = %q, want think=high", res.Identity.Variant)
	}
}

// The openai provider is the one whose target is configurable, so where a
// request actually lands is a property worth asserting on the bytes: a base URL
// only serves an OpenAI-compatible gateway if the path is appended to it.
func TestOpenAIRequestWire(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	var path string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		path = r.URL.Path
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"SERVED"}}],"usage":{"prompt_tokens":33,"completion_tokens":44}}`))
	}))
	defer srv.Close()

	runner, err := gollmrunner.NewRunner(model.LLMConfig{
		Provider: "openai", Model: "m", Endpoint: srv.URL,
		APIKeys: map[string]string{"openai": "op-notarealkeyaaaaaaaaaa"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), llm.Request{
		SystemPrompt: "SYSTEM-BLOCK-MARKER",
		UserPrompt:   "USER-BLOCK-MARKER",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("request keys: %v", keysOf(body))

	if path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", path)
	}
	// A key OpenAI itself would reject must still reach a gateway that issued it.
	sent, _ := json.Marshal(body["messages"])
	for _, marker := range []string{"SYSTEM-BLOCK-MARKER", "USER-BLOCK-MARKER"} {
		if !strings.Contains(string(sent), marker) {
			t.Errorf("%s missing from the messages sent: %s", marker, sent)
		}
	}
	if res.Text != "SERVED" {
		t.Errorf("text = %q, want SERVED", res.Text)
	}
	if res.Usage.InputTokens != 33 || res.Usage.OutputTokens != 44 {
		t.Errorf("usage not parsed: %+v", res.Usage)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The structured-output key differs per provider and the option map is copied
// into the body verbatim, so only the bytes prove the schema reached the
// OpenAI-compatible wire under the name that provider reads.
func TestOpenAICompatibleSchemaAndEndpointWire(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	var path string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		path = r.URL.Path
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`))
	}))
	defer srv.Close()

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"findings": map[string]any{"type": "array"}},
	}
	runner, err := gollmrunner.NewRunner(model.LLMConfig{
		Provider: "openai", Model: "gpt-5", Endpoint: srv.URL,
		APIKeys: map[string]string{"openai": "sk-testkeyaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}, gollmrunner.WithStructuredOutput("preflight_findings", schema))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), llm.Request{UserPrompt: "USER-BLOCK-MARKER"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if path != "/v1/chat/completions" {
		t.Errorf("endpoint override not honored: request hit %q", path)
	}
	format, ok := body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format missing: %v", keysOf(body))
	}
	if format["type"] != "json_schema" {
		t.Errorf("response_format type = %v, want json_schema", format["type"])
	}
	inner, ok := format["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("json_schema missing: %v", format)
	}
	if inner["name"] != "preflight_findings" || inner["strict"] != true {
		t.Errorf("json_schema name/strict = %v/%v", inner["name"], inner["strict"])
	}
	if _, ok := inner["schema"].(map[string]any); !ok {
		t.Errorf("schema not carried: %v", inner)
	}
	if res.Usage.InputTokens != 5 || res.Usage.OutputTokens != 7 {
		t.Errorf("usage not parsed: %+v", res.Usage)
	}
}
