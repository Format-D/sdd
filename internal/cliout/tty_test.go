package cliout

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSuppliedStreamsWithoutTerminalDescriptors(t *testing.T) {
	for name, stream := range map[string]any{
		"nil":      nil,
		"nil file": (*os.File)(nil),
		"reader":   strings.NewReader("input"),
		"writer":   &bytes.Buffer{},
	} {
		t.Run(name, func(t *testing.T) {
			if IsTerminal(stream) || IsInteractive(stream) {
				t.Fatal("a stream without a terminal descriptor must use plain output")
			}
		})
	}
}

func TestIsInteractive_NilFile(t *testing.T) {
	if IsInteractive(nil) {
		t.Error("nil file must not be interactive")
	}
}

func TestIsInteractive_Pipe(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer rd.Close()
	defer wr.Close()

	// A pipe is not a terminal — both ends take the plain path.
	if IsInteractive(wr) {
		t.Error("pipe write end must not be interactive")
	}
	if IsInteractive(rd) {
		t.Error("pipe read end must not be interactive")
	}
}

func TestIsInteractive_NoColorForcesFalse(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Even if stderr happened to be a TTY in some environment, NO_COLOR forces
	// the plain path.
	if IsInteractive(os.Stderr) {
		t.Error("NO_COLOR set must force non-interactive")
	}
}
