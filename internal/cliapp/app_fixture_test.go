package cliapp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/networkteam/sdd/internal/cliapp"
	"github.com/urfave/cli/v3"
)

const (
	appVersion = "1.2.3-test"
	appEntryID = "20260914-020000-s-tac-cli"
	appSummary = "Configured CLI reads use the selected graph."
	appBody    = "The command renders this entry through the configured graph directory."
)

type appFixture struct{ root string }

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	f := &appFixture{root: t.TempDir()}
	t.Chdir(f.root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(f.root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(f.root, "state"))
	f.write(t, "config/sdd/config.yaml", "participant: Global Author\n")
	f.write(t, ".sdd/config.yaml", "repo_id: example.test/cli\ngraph_dir: decisions\ndefault_branch: main\nlanguage: de\n")
	f.write(t, ".sdd/config.local.yaml", "participant: Local Author\n")
	f.write(t, "decisions/2026/09/14-020000-s-tac-cli.md", "---\ntype: signal\nkind: gap\nlayer: tactical\nconfidence: high\nparticipants: [Local Author]\nsummary: "+appSummary+"\n---\n\n"+appBody+"\n")
	return f
}

func (f *appFixture) write(t *testing.T, name, content string) {
	t.Helper()
	filename := filepath.Join(f.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type appResult struct {
	stdout, stderr string
	err            error
}

func (f *appFixture) run(t *testing.T, args ...string) appResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := f.command(strings.NewReader(""), &stdout, &stderr)
	err := command.Run(t.Context(), append([]string{"sdd"}, args...))
	return appResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (f *appFixture) command(reader io.Reader, writer, errWriter io.Writer) *cli.Command {
	return cliapp.New(cliapp.Options{
		Version: appVersion, Reader: reader, Writer: writer, ErrWriter: errWriter,
	})
}

func (r appResult) exitCode() int {
	if r.err == nil {
		return 0
	}
	var exit cli.ExitCoder
	if errors.As(r.err, &exit) {
		return exit.ExitCode()
	}
	return 1
}

func assertContainsOutput(t *testing.T, stream, output string, expected ...string) {
	t.Helper()
	for _, text := range expected {
		if !strings.Contains(output, text) {
			t.Errorf("%s omitted %q:\n%s", stream, text, output)
		}
	}
}

func (f *appFixture) serve(t *testing.T, check func(*mcp.ClientSession)) {
	t.Helper()
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var stderr bytes.Buffer
	command := f.command(serverInput, serverOutput, &stderr)
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = command.Run(ctx, []string{"sdd", "serve", "--transport", "stdio"})
		close(done)
	}()
	defer func() {
		cancel()
		for _, pipe := range []io.Closer{serverInput, clientOutput, clientInput, serverOutput} {
			if err := pipe.Close(); err != nil {
				t.Error(err)
			}
		}
		select {
		case <-done:
			if t.Failed() {
				t.Logf("sdd serve: %v\n%s", runErr, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("sdd serve did not stop after closing its streams")
		}
	}()

	client, err := mcp.NewClient(&mcp.Implementation{Name: "cli-test", Version: "test"}, nil).Connect(ctx, &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}, nil)
	if err != nil {
		t.Fatalf("initialize CLI MCP: %v", err)
	}
	check(client)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if runErr != nil {
			t.Fatalf("CLI Run after MCP disconnect: %v\n%s", runErr, stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("CLI Run did not return after MCP disconnect")
	}
}
