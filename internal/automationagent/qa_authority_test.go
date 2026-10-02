package automationagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestQACommandCancelsInFlightWhenAuthorityIsRevoked(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "unavailable"}[transient], func(t *testing.T) {
			var calls atomic.Int64
			started := make(chan struct{})
			finished := make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := runQACommandWithAuthorityInterval(ctx, func(context.Context) (bool, error) {
				<-started
				calls.Add(1)
				if transient {
					return false, errors.New("synthetic callback unavailable")
				}
				return false, nil
			}, func(commandCtx context.Context) (commandResult, error) {
				close(started)
				<-commandCtx.Done()
				close(finished)
				return commandResult{}, commandCtx.Err()
			}, 10*time.Millisecond)
			var authorityErr *qaCapabilityRefreshError
			if !errors.As(err, &authorityErr) || calls.Load() != 1 {
				t.Fatalf("command lost its authority cause: %v, refreshes=%d", err, calls.Load())
			}
			if !transient && !errors.Is(err, errQACapabilityNotAccepted) {
				t.Fatal("revocation became an ordinary tool failure")
			}
			select {
			case <-finished:
			default:
				t.Fatal("command remained active after lease revocation")
			}
		})
	}
}

func TestQACommandStopsAuthorityHeartbeatAfterCompletion(t *testing.T) {
	var calls atomic.Int64
	_, err := runQACommandWithAuthorityInterval(context.Background(), func(context.Context) (bool, error) { calls.Add(1); return true, nil }, func(context.Context) (commandResult, error) { return commandResult{ExitCode: 0}, nil }, time.Second)
	if err != nil || calls.Load() != 0 {
		t.Fatalf("completed command left an authority heartbeat: %v", err)
	}
}
