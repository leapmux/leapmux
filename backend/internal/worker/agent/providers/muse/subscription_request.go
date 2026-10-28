package muse

import (
	"context"
	"encoding/json"
	"fmt"
)

type nativeSubscriptionResult struct {
	raw json.RawMessage
	err error
}

type nativeSubscriptionCall struct {
	result chan nativeSubscriptionResult
}

// Operation cancellation ends the caller's wait. The process still owns the
// native acknowledgement and its admission fence until the reply or process exit.
func (a *Agent) awaitOwnedSubscription(ctx context.Context, release func(), method string, params any, observe func(json.RawMessage) error) (json.RawMessage, error) {
	if ctx.Err() != nil {
		release()
		return nil, context.Cause(ctx)
	}
	call := &nativeSubscriptionCall{result: make(chan nativeSubscriptionResult, 1)}
	admitted := a.subscriptionWork.Run(fmt.Sprintf("%p", call), func() {
		defer release()
		if ctx.Err() != nil {
			call.result <- nativeSubscriptionResult{err: context.Cause(ctx)}
			return
		}
		var observedErr error
		raw, err := a.request(method, params, 0, func(raw json.RawMessage, nativeErr error) {
			if nativeErr == nil && observe != nil {
				observedErr = observe(raw)
			}
		})
		if err != nil {
			// Cancellation can release the wait while a claimed observer still runs.
			call.result <- nativeSubscriptionResult{err: err}
			return
		}
		call.result <- nativeSubscriptionResult{raw: raw, err: observedErr}
	})
	if !admitted {
		release()
		return nil, fmt.Errorf("the Muse subscription worker is closed")
	}
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case result := <-call.result:
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return result.raw, result.err
	}
}
