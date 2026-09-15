package cliout

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// IsTerminalReader reports whether the reader is an interactive terminal.
// In-memory readers and other streams without a descriptor are not.
func IsTerminalReader(reader io.Reader) bool { return hasTerminalDescriptor(reader) }

// IsTerminalWriter reports whether the writer is an interactive terminal.
func IsTerminalWriter(writer io.Writer) bool { return hasTerminalDescriptor(writer) }

func hasTerminalDescriptor(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}

// IsInteractive reports whether writer is an interactive terminal suitable for a
// transient TUI. It returns false for non-terminals (pipes, files, /dev/null,
// agent stdio) and when NO_COLOR is set — both take the plain leveled path,
// so agents and piped consumers never see an alt-screen program. term.IsTerminal
// is used rather than a file-mode check because special devices like /dev/null
// are character devices but not terminals, and bubble tea opens /dev/tty
// directly and fails in non-interactive contexts.
func IsInteractive(writer io.Writer) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return IsTerminalWriter(writer)
}
