package cliout

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestStreamsWithoutTerminalDescriptorsAreNotTerminals(t *testing.T) {
	if IsTerminalReader(nil) || IsTerminalReader(strings.NewReader("input")) || IsTerminalReader((*os.File)(nil)) {
		t.Fatal("a reader without a terminal descriptor must use plain input")
	}
	if IsTerminalWriter(nil) || IsTerminalWriter(&bytes.Buffer{}) || IsInteractive(&bytes.Buffer{}) || IsInteractive(nil) {
		t.Fatal("a writer without a terminal descriptor must use plain output")
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
	if IsTerminalReader(rd) {
		t.Error("pipe read end must not be a terminal")
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
