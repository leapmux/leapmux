package acp

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildSessionDispatchDrainsEveryAdmittedOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var dispatch childSessionDispatch
		first, admitted := dispatch.acquire(t.Context())
		require.True(t, admitted)
		defer first()
		second, admitted := dispatch.acquire(t.Context())
		require.True(t, admitted)
		defer second()
		paused := make(chan func(), 1)
		go func() { paused <- dispatch.pause() }()
		synctest.Wait()
		select {
		case resume := <-paused:
			resume()
			t.Fatal("context replacement overtook admitted operations")
		default:
		}
		first()
		synctest.Wait()
		select {
		case resume := <-paused:
			resume()
			t.Fatal("context replacement overtook the second operation")
		default:
		}
		second()
		synctest.Wait()
		var resume func()
		select {
		case resume = <-paused:
		default:
			t.Fatal("context replacement did not acquire its drained lease")
		}
		defer resume()
		queued := make(chan func(), 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			release, _ := dispatch.acquire(ctx)
			queued <- release
		}()
		synctest.Wait()
		select {
		case release := <-queued:
			if release != nil {
				release()
			}
			t.Fatal("a new operation entered the paused session")
		default:
		}
		resume()
		synctest.Wait()
		release := <-queued
		require.NotNil(t, release)
		release()
		release()
	})
}

func TestChildSessionDispatchCancelsAWaitingOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var dispatch childSessionDispatch
		resume := dispatch.pause()
		defer resume()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan bool, 1)
		go func() {
			release, admitted := dispatch.acquire(ctx)
			if release != nil {
				release()
			}
			result <- admitted
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		assert.False(t, <-result)
		resume()
		release, admitted := dispatch.acquire(t.Context())
		require.True(t, admitted)
		release()
		_, admitted = dispatch.acquire(ctx)
		assert.False(t, admitted)
	})
}

func TestChildSessionDispatchSerializesTransitionsAndIgnoresRepeatedRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var dispatch childSessionDispatch
		first := dispatch.pause()
		defer first()
		second := make(chan func(), 1)
		go func() { second <- dispatch.pause() }()
		synctest.Wait()
		select {
		case resume := <-second:
			resume()
			t.Fatal("two context transitions acquired the same session")
		default:
		}
		first()
		synctest.Wait()
		resume := <-second
		defer resume()
		first()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		queued := make(chan bool, 1)
		go func() {
			release, admitted := dispatch.acquire(ctx)
			if release != nil {
				release()
			}
			queued <- admitted
		}()
		synctest.Wait()
		select {
		case <-queued:
			t.Fatal("an old release ended the replacement transition")
		default:
		}
		resume()
		synctest.Wait()
		assert.True(t, <-queued)
	})
}
