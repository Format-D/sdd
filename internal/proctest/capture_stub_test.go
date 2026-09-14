package proctest_test

import (
	"context"
	"errors"

	sdd "github.com/networkteam/sdd/pkg/application"
)

type captureCompletionFailure struct{ calls int }

func (*captureCompletionFailure) Name() string { return "capture-completion" }
func (f *captureCompletionFailure) Finalize(context.Context, sdd.AppliedMutation) error {
	f.calls++
	if f.calls == 1 {
		return errors.New("completion unavailable after publication")
	}
	return nil
}
