package factory

import (
	"context"
	"fmt"
	"sync"

	gollmrunner "github.com/networkteam/sdd/internal/llm/gollm"
	"github.com/networkteam/sdd/internal/llmops"
	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/pkg/llm"
)

// structuredPurposes names the schema each purpose constrains its response to.
// A purpose absent here is served unconstrained, which is what summarize needs
// since it answers in prose.
var structuredPurposes = map[llm.Purpose]func() (string, map[string]any){
	llm.PurposePreflight:    func() (string, map[string]any) { return llmops.PreflightSchemaName, llmops.PreflightSchema() },
	llm.PurposeWritingGuide: func() (string, map[string]any) { return llmops.WritingGuideSchemaName, llmops.WritingGuideSchema() },
}

// purposeMux routes a call to the client configured for its purpose. A gollm
// client carries its structured-output schema for the life of the client
// (options are client-level, not per call), so one client per schema is what
// keeps a JSON-constrained pre-flight and a prose summarize on the same
// configuration.
//
// The unconstrained client is built eagerly, so a misconfiguration (a missing
// API key, an unreachable Ollama) fails the command rather than the first call
// that happens to need a checker. Schema-carrying clients are built on first
// use, because constructing one probes the endpoint on the Ollama provider and
// a command that only summarizes never calls a checker.
type purposeMux struct {
	cfg model.LLMConfig

	mu      sync.Mutex
	clients map[llm.Purpose]llm.Runner
}

func newPurposeMux(cfg model.LLMConfig) (*purposeMux, error) {
	base, err := gollmrunner.NewRunner(cfg)
	if err != nil {
		return nil, err
	}
	return &purposeMux{cfg: cfg, clients: map[llm.Purpose]llm.Runner{"": base}}, nil
}

func (m *purposeMux) Run(ctx context.Context, req llm.Request) (llm.Result, error) {
	runner, err := m.runnerFor(req.Purpose)
	if err != nil {
		return llm.Result{}, err
	}
	return runner.Run(ctx, req)
}

func (m *purposeMux) runnerFor(purpose llm.Purpose) (llm.Runner, error) {
	key := schemaKey(purpose)

	m.mu.Lock()
	defer m.mu.Unlock()
	if runner, ok := m.clients[key]; ok {
		return runner, nil
	}

	var opts []gollmrunner.Option
	if schema, ok := structuredPurposes[key]; ok {
		name, definition := schema()
		opts = append(opts, gollmrunner.WithStructuredOutput(name, definition))
	}
	runner, err := gollmrunner.NewRunner(m.cfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("llm: building runner for purpose %q: %w", purpose, err)
	}
	m.clients[key] = runner
	return runner, nil
}

// schemaKey collapses every unconstrained purpose onto one client, so a
// process summarizing and checking holds two clients rather than one per
// purpose value.
func schemaKey(purpose llm.Purpose) llm.Purpose {
	if _, ok := structuredPurposes[purpose]; ok {
		return purpose
	}
	return ""
}
