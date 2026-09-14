package tui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/networkteam/sdd/internal/cliout"
	sddmodel "github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/slogutils"
)

func TestInteractivePlainLogsUseSuppliedWriter(t *testing.T) {
	var output bytes.Buffer
	result, err := Interactive(t.Context(), cliout.Policy{Display: slog.LevelInfo}, View{
		Reader: strings.NewReader(""), Writer: &output, StreamLogs: true,
	}, func(ctx context.Context) (int, error) {
		slogutils.FromContext(ctx).Info("supplied output")
		return 42, nil
	})
	if err != nil || result != 42 {
		t.Fatalf("work = %d, %v", result, err)
	}
	if !strings.Contains(output.String(), "supplied output") {
		t.Fatalf("supplied writer received %q", output.String())
	}
}

func TestInteractiveLiveViewReadsCancellationFromSuppliedReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	_, err := Interactive(ctx, cliout.Policy{Display: slog.LevelInfo}, View{
		Reader: strings.NewReader("\x03"), Writer: &output, StreamLogs: true,
		InitialPhase: sddmodel.PhaseIndexing,
	}, func(workCtx context.Context) (int, error) {
		slogutils.FromContext(workCtx).Info("waiting for cancellation")
		<-workCtx.Done()
		return 0, workCtx.Err()
	})
	if !errors.Is(err, cliout.ErrUserCancelled) || ctx.Err() != nil {
		t.Fatalf("cancellation = %v; outer context = %v", err, ctx.Err())
	}
	if !strings.Contains(output.String(), "waiting for cancellation") {
		t.Fatalf("supplied writer received %q", output.String())
	}
}

// recordingRun stands in for the real program runner and flags whether a
// program was ever started — instant (dormant/armed-fast) operations must not
// start one.
func recordingRun() (programRunner, *bool) {
	started := false
	return func(m tea.Model) (tea.Model, error) {
		started = true
		return m, nil
	}, &started
}

func TestInteractive_ReturnsResultForInstantWork(t *testing.T) {
	policy := cliout.Policy{Display: slog.LevelInfo, KeepAtOrAbove: slog.LevelWarn}
	work := func(context.Context) (int, error) { return 42, nil }

	run, started := recordingRun()
	val, err := interactiveWith(context.Background(), policy, View{InitialPhase: sddmodel.PhaseIndexing}, work, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 42 {
		t.Errorf("result = %d, want 42", val)
	}
	if *started {
		t.Error("instant work must not start a program")
	}
}

func TestInteractive_PropagatesWorkError(t *testing.T) {
	policy := cliout.Policy{Display: slog.LevelInfo, KeepAtOrAbove: slog.LevelWarn}
	boom := errors.New("boom")
	work := func(context.Context) (int, error) { return 0, boom }

	run, _ := recordingRun()
	_, err := interactiveWith(context.Background(), policy, View{InitialPhase: sddmodel.PhaseIndexing}, work, run)
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
}

func TestInteractive_TranslatesCancellationToSentinel(t *testing.T) {
	policy := cliout.Policy{Display: slog.LevelInfo}
	work := func(context.Context) (int, error) { return 0, context.Canceled }

	run, _ := recordingRun()
	_, err := interactiveWith(context.Background(), policy, View{InitialPhase: sddmodel.PhaseIndexing}, work, run)
	if !errors.Is(err, cliout.ErrUserCancelled) {
		t.Errorf("error = %v, want ErrUserCancelled", err)
	}
}
