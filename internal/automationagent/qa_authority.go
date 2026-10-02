package automationagent

import (
	"context"
	"errors"
	"time"
)

const qaExecutionAuthorityInterval = 5 * time.Second

func runQAStageWithAuthority(ctx context.Context, refresh func(context.Context) (bool, error), run func(context.Context) error) error {
	_, err := runQACommandWithAuthority(ctx, refresh, func(stageCtx context.Context) (commandResult, error) { return commandResult{}, run(stageCtx) })
	return err
}

// The command's own deadline still applies. This heartbeat only narrows that
// lifetime when the live server lease is revoked or cannot be refreshed.
func runQACommandWithAuthority(ctx context.Context, refresh func(context.Context) (bool, error), run func(context.Context) (commandResult, error)) (commandResult, error) {
	return runQACommandWithAuthorityInterval(ctx, refresh, run, qaExecutionAuthorityInterval)
}

func runQACommandWithAuthorityInterval(ctx context.Context, refresh func(context.Context) (bool, error), run func(context.Context) (commandResult, error), interval time.Duration) (commandResult, error) {
	if refresh == nil {
		return run(ctx)
	}
	commandCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-commandCtx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				refreshCtx, finish := context.WithTimeout(commandCtx, qaExecutionAuthorityInterval)
				err := refreshQAExecutionAuthority(refreshCtx, refresh)
				if err == nil && refreshCtx.Err() != nil {
					err = &qaCapabilityRefreshError{cause: refreshCtx.Err()}
				}
				finish()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	result, err := run(commandCtx)
	close(stop)
	<-done
	var authorityErr *qaCapabilityRefreshError
	if errors.As(context.Cause(commandCtx), &authorityErr) {
		return result, authorityErr
	}
	return result, err
}
