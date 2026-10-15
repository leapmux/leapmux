import { batch, createComputed, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'

describe('createLoadingSignal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('updates the pending count and axes together for reactive readers', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      const snapshots: { pending: boolean, axes: string[] }[] = []
      createComputed(() => {
        snapshots.push({ pending: signal.isPending('A'), axes: [...signal.pendingAxes('A')] })
      })

      signal.start('A', ['model'])
      signal.start('A', ['effort'])
      signal.stop('A', ['model'])
      signal.stop('A', ['effort'])

      expect(snapshots).toEqual([
        { pending: false, axes: [] },
        { pending: true, axes: ['model'] },
        { pending: true, axes: ['model', 'effort'] },
        { pending: true, axes: ['effort'] },
        { pending: false, axes: [] },
      ])
      dispose()
    })
  })

  it('reads pending axes without a reactive subscription', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      let reads = 0
      createComputed(() => {
        signal.pendingAxes('A')
        reads += 1
      })
      signal.start('A', ['model'])
      signal.stop('A', ['model'])
      expect(reads).toBe(1)
      dispose()
    })
  })

  it('extends the agent timeout when another change starts', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('A', ['model'])
      vi.advanceTimersByTime(4000)
      signal.start('A', ['effort'])
      signal.stop('A', ['model'])
      vi.advanceTimersByTime(1000)
      expect(signal.isPending('A')).toBe(true)
      expect([...signal.pendingAxes('A')]).toEqual(['effort'])
      vi.advanceTimersByTime(4000)
      expect(signal.isPending('A')).toBe(false)
      expect([...signal.pendingAxes('A')]).toEqual([])
      expect(vi.getTimerCount()).toBe(0)
      dispose()
    })
  })

  it('clears every timer when Solid disposes the reactive root', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start('A', ['model'])
      signal.start('B', ['effort'])
      expect(vi.getTimerCount()).toBe(4)
      dispose()
      expect(vi.getTimerCount()).toBe(0)
    })
  })

  it('removes all axes after the last change even when the stop axes differ', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start('', ['model'])
      signal.stop('', ['effort'])
      expect(signal.isPending('')).toBe(false)
      expect([...signal.pendingAxes('')]).toEqual([])
      vi.advanceTimersByTime(1000)
      expect(vi.getTimerCount()).toBe(0)
      dispose()
    })
  })

  it('should start in non-loading state', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should be loading after start()', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)
      dispose()
    })
  })

  it('should remain loading during debounce window even after stop()', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)

      // Call stop() before the debounce timer expires.
      // The spinner must remain visible until that timer expires.
      signal.stop()
      expect(signal.loading()).toBe(true)

      // After the debounce period (1000ms), the deferred stop takes effect.
      vi.advanceTimersByTime(1000)
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should stop immediately when debounce has already elapsed', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)

      // Advance past the debounce window.
      vi.advanceTimersByTime(1000)
      expect(signal.loading()).toBe(true)

      // Now stop() should take effect immediately.
      signal.stop()
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should auto-stop after timeout', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start()
      expect(signal.loading()).toBe(true)

      vi.advanceTimersByTime(5000)
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('isPending(agentId) is scoped per-agent so one agent\'s change does not suppress another', () => {
    // The statusChange handler must protect only the agent with a pending change.
    // A pending change for agent A must not suppress confirmed values for agent B.
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)
      // Agent B has no change in flight — its pushes must apply.
      expect(signal.isPending('agent-B')).toBe(false)
      // Global spinner is still on (a change is loading somewhere).
      expect(signal.loading()).toBe(true)

      // Resolving A's change clears only A's pending marker.
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('isPending tracks multiple concurrent agents independently', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start('agent-A')
      signal.start('agent-B')
      expect(signal.isPending('agent-A')).toBe(true)
      expect(signal.isPending('agent-B')).toBe(true)

      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(false)
      expect(signal.isPending('agent-B')).toBe(true)
      dispose()
    })
  })

  it('keeps an agent pending until ALL its concurrent changes settle (refcount, not membership)', () => {
    // Concurrent changes keep an agent pending until its last request completes.
    // A set cannot represent the remaining changes after the first request completes.
    // Early removal would let a status update overwrite pending optimistic values.
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      // Three concurrent changes for the same agent.
      signal.start('agent-A')
      signal.start('agent-A')
      signal.start('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)

      // First RPC settles -- two still in flight.
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)

      // Second settles -- one still in flight.
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)

      // Last settles -- now clear.
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('the safety-net timeout force-clears an agent even with several changes in flight', () => {
    // The timeout removes every pending change for an unresponsive agent.
    // It must not decrement the count by only one.
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('agent-A')
      signal.start('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)

      // Neither stop() arrives; the timeout is the safety net.
      vi.advanceTimersByTime(5000)
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('an extra stop() for an agent never drives its count negative', () => {
    // A duplicate stop() must not decrement the count below zero.
    // This can occur when a rollback and a completion both call stop().
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start('agent-A')
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(false)
      // Extra stop with nothing in flight is a no-op, not an underflow.
      signal.stop('agent-A')
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('timeout clears pending agents so a hung change can\'t strand one forever', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('agent-A')
      expect(signal.isPending('agent-A')).toBe(true)

      // stop() never arrives; the timeout is the safety net.
      vi.advanceTimersByTime(5000)
      expect(signal.isPending('agent-A')).toBe(false)
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('a keyless stop() must not cancel another agent\'s pending safety-net timeout', () => {
    // useWorkspaceConnection calls a keyless stop() for updates from agents without pending changes.
    // A shared timeout would let that call cancel another agent's timeout.
    // That agent would then stay pending and suppress confirmed settings indefinitely.
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('agent-A') // A's change in flight; A's safety net armed.
      expect(signal.isPending('agent-A')).toBe(true)

      // Advance past the debounce period so stop() can clear the spinner immediately.
      vi.advanceTimersByTime(1000)

      // An unrelated, non-pending agent's status push fires the keyless stop().
      signal.stop()

      // Agent A never calls stop(). Its timeout must still clear its pending state.
      vi.advanceTimersByTime(5000)
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('each agent\'s safety-net timeout is independent — a later start() does not strand an earlier agent', () => {
    // Each agent must retain its own timeout when another agent starts a change.
    // A timeout must clear only its own agent.
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('agent-A') // A armed at t=0, should time out at t=5000.
      vi.advanceTimersByTime(4000) // t=4000
      signal.start('agent-B') // B armed at t=4000, should time out at t=9000.

      vi.advanceTimersByTime(1000) // t=5000: A's safety net fires.
      expect(signal.isPending('agent-A')).toBe(false) // A timed out on its own schedule.
      expect(signal.isPending('agent-B')).toBe(true) // B still has 4s left -- must survive.

      vi.advanceTimersByTime(4000) // t=9000: B's safety net fires.
      expect(signal.isPending('agent-B')).toBe(false)
      dispose()
    })
  })

  it('keeps the spinner visible during the debounce period after a keyless stop()', () => {
    // A keyless stop() keeps the spinner visible until the debounce period ends.
    createRoot((dispose) => {
      const settingsLoading = createLoadingSignal()

      // Start a settings change.
      settingsLoading.start()
      expect(settingsLoading.loading()).toBe(true)

      // Read the spinner state before the debounce period ends.
      const pendingSettings = settingsLoading.loading()
      expect(pendingSettings).toBe(true)

      // Simulate a keyless stop from a status update before the debounce period ends.
      settingsLoading.stop()
      expect(settingsLoading.loading()).toBe(true)

      // After debounce, loading clears.
      vi.advanceTimersByTime(1000)
      expect(settingsLoading.loading()).toBe(false)
      dispose()
    })
  })

  it('an unrelated agent\'s stop() must not clear the spinner while another agent is still pending', () => {
    // The spinner timers are shared.
    // A fast change for agent B must not hide the spinner while agent A has a pending change.
    createRoot((dispose) => {
      const s = createLoadingSignal()
      s.start('A') // A's change begins; debounce armed.
      expect(s.loading()).toBe(true)

      s.start('B') // B's change begins (re-arms the shared debounce).
      s.stop('B') // B settles fast, within the debounce window, while A is still pending.

      // A is still pending, so the spinner must stay on through the debounce fire.
      vi.advanceTimersByTime(1000)
      expect(s.isPending('A')).toBe(true)
      expect(s.loading()).toBe(true)

      // Once A settles too, the spinner clears after the debounce.
      s.stop('A')
      vi.advanceTimersByTime(1000)
      expect(s.isPending('A')).toBe(false)
      expect(s.loading()).toBe(false)
      dispose()
    })
  })

  it('a keyless stop() with no pending agents still clears the spinner (no regression)', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      s.start() // keyless: no per-agent count.
      expect(s.loading()).toBe(true)
      s.stop() // keyless stop with an empty pending map proceeds normally.
      vi.advanceTimersByTime(1000)
      expect(s.loading()).toBe(false)
      dispose()
    })
  })

  // The stop() decision uses the same update that decrements the pending count.
  // This keeps the spinner decision consistent with the pending state inside a Solid batch().
  it('stop() inside a batch() clears the spinner for the last pending agent', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      s.start('A')
      s.start('B')
      // Advance past the debounce so a settling stop() takes effect immediately rather
      // than arming the deferred path.
      vi.advanceTimersByTime(1000)
      expect(s.loading()).toBe(true)

      // Stopping A (not the last) keeps the spinner up while B is still pending.
      s.stop('A')
      expect(s.loading()).toBe(true)

      // The last agent completes inside a batch. The spinner must clear.
      batch(() => s.stop('B'))
      expect(s.loading()).toBe(false)
      dispose()
    })
  })

  // pendingAxes lets useWorkspaceConnection protect optimistic values for individual axes.
  // A server change to another axis on the same agent must still apply.
  describe('pendingAxes', () => {
    it('reports exactly the axes a change is in flight for, empty when none', () => {
      createRoot((dispose) => {
        const s = createLoadingSignal()
        expect([...s.pendingAxes('A')]).toEqual([])
        s.start('A', ['model', 'effort'])
        expect(new Set(s.pendingAxes('A'))).toEqual(new Set(['model', 'effort']))
        s.stop('A', ['model', 'effort'])
        expect([...s.pendingAxes('A')]).toEqual([])
        dispose()
      })
    })

    it('does not report an axis the agent is NOT changing (the core per-axis property)', () => {
      createRoot((dispose) => {
        const s = createLoadingSignal()
        s.start('A', ['permissionMode'])
        // Only permissionMode is pending; a server push for model on the same agent is free to apply.
        expect(s.pendingAxes('A').has('permissionMode')).toBe(true)
        expect(s.pendingAxes('A').has('model')).toBe(false)
        dispose()
      })
    })

    it('is scoped per-agent', () => {
      createRoot((dispose) => {
        const s = createLoadingSignal()
        s.start('A', ['model'])
        expect(s.pendingAxes('A').has('model')).toBe(true)
        expect(s.pendingAxes('B').has('model')).toBe(false)
        dispose()
      })
    })

    it('keeps an axis pending until every concurrent change touching it settles (refcount)', () => {
      createRoot((dispose) => {
        const s = createLoadingSignal()
        // Two concurrent changes both touch effort; one also touches model.
        s.start('A', ['model', 'effort'])
        s.start('A', ['effort'])
        expect(new Set(s.pendingAxes('A'))).toEqual(new Set(['model', 'effort']))

        // The first change completes, so model is no longer pending.
        // Effort remains pending until the second change completes.
        s.stop('A', ['model', 'effort'])
        expect(new Set(s.pendingAxes('A'))).toEqual(new Set(['effort']))

        s.stop('A', ['effort'])
        expect([...s.pendingAxes('A')]).toEqual([])
        dispose()
      })
    })

    it('the safety-net timeout clears pending axes too', () => {
      createRoot((dispose) => {
        const s = createLoadingSignal(5000)
        s.start('A', ['model'])
        expect(s.pendingAxes('A').has('model')).toBe(true)
        // No stop() arrives; the hung-RPC safety net must drop the axes as well as the count.
        vi.advanceTimersByTime(5000)
        expect([...s.pendingAxes('A')]).toEqual([])
        dispose()
      })
    })
  })
})
