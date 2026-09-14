package cliout

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// IsTerminal checks the supplied stream's file descriptor. In-memory streams
// and other streams without a descriptor are not terminals.
func IsTerminal(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}

// IsInteractive reports whether stream is an interactive terminal suitable for a
// transient TUI. It returns false for non-terminals (pipes, files, /dev/null,
// agent stdio) and when NO_COLOR is set — both take the plain leveled path,
// so agents and piped consumers never see an alt-screen program. term.IsTerminal
// is used rather than a file-mode check because special devices like /dev/null
// are character devices but not terminals, and bubble tea opens /dev/tty
// directly and fails in non-interactive contexts.
func IsInteractive(stream any) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return IsTerminal(stream)
}
