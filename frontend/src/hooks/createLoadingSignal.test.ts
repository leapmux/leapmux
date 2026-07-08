import { batch, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'

describe('createLoadingSignal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
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

      // Call stop() immediately — within the debounce window.
      // loading() should still be true because the debounce hasn't fired yet.
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
    // Regression: the statusChange handler must suppress optimistic-value overwrites
    // only for the agent whose change is in flight. A change pending on agent A must
    // NOT make agent B's unrelated status push drop B's confirmed current values.
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
    // Regression: a multi-write action (Codex "Bypass permissions" fires three
    // updateAgentSettings RPCs for one agent at once) -- and rapid back-to-back
    // changes -- must keep the agent pending until the LAST RPC settles. With set
    // membership, the first resolving RPC cleared the shared marker while the others
    // were still in flight, letting a status push overwrite the still-optimistic
    // values mid-batch.
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
    // A hung multi-write must not strand the agent: the timeout abandons the whole
    // refcount, not just one in-flight change.
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
    // A stray/duplicate stop() (e.g. a rollback racing a resolve) must not make the
    // agent read as "still pending" by underflowing the count below zero.
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
    // Regression: useWorkspaceConnection calls settingsLoading.stop() (no key) on every
    // non-pending agent status push (git updates, ACTIVE/INACTIVE transitions). With a
    // single shared safety-net timer, that keyless stop cancelled the timer guarding a
    // DIFFERENT agent's pending state, so if that agent's own stop() never arrived (hung
    // RPC) it stayed isPending forever and every confirming statusChange for it was
    // suppressed -- freezing its settings UI on the optimistic value.
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start('agent-A') // A's change in flight; A's safety net armed.
      expect(signal.isPending('agent-A')).toBe(true)

      // Advance past the debounce so a later keyless stop() takes the immediate path
      // (which, in the buggy version, called the shared clearTimers()).
      vi.advanceTimersByTime(1000)

      // An unrelated, non-pending agent's status push fires the keyless stop().
      signal.stop()

      // A's RPC hangs: its stop('agent-A') never arrives. A's own safety net must still
      // fire and clear it, rather than having been cancelled by the keyless stop().
      vi.advanceTimersByTime(5000)
      expect(signal.isPending('agent-A')).toBe(false)
      dispose()
    })
  })

  it('each agent\'s safety-net timeout is independent — a later start() does not strand an earlier agent', () => {
    // Regression: a single shared timer meant start('B') cancelled A's safety-net timer
    // (and the timer's fire did setPendingKeys(new Set()), wiping ALL agents at once).
    // Each agent must own its safety net so it times out on its own schedule.
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

  it('loading() returns true during debounce — guards statusChange from overwriting settings', () => {
    // This test validates the invariant used by the statusChange handler:
    // when settingsLoading.loading() is true, incoming statusChange events
    // should not overwrite optimistically-set settings fields.
    createRoot((dispose) => {
      const settingsLoading = createLoadingSignal()

      // Simulate: user changes permission mode → optimistic update + start()
      settingsLoading.start()
      expect(settingsLoading.loading()).toBe(true)

      // Simulate: statusChange arrives from agent restart (within debounce).
      // The guard checks loading() — it should be true, so settings are skipped.
      const pendingSettings = settingsLoading.loading()
      expect(pendingSettings).toBe(true)

      // Simulate: the statusChange handler calls stop() (as the old code did).
      // Even so, loading should still be true during the debounce window.
      settingsLoading.stop()
      expect(settingsLoading.loading()).toBe(true)

      // After debounce, loading clears.
      vi.advanceTimersByTime(1000)
      expect(settingsLoading.loading()).toBe(false)
      dispose()
    })
  })

  it('an unrelated agent\'s stop() must not clear the spinner while another agent is still pending', () => {
    // The spinner timers are shared across keys; without the "any pending" guard, a
    // fast change for agent B (start+stop within A's debounce window) would arm the
    // shared stopRequested and clear loading() out from under A, whose change is still
    // in flight.
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

  // Pins the batch-safety property: stop() must decide whether any agent is still pending
  // from the SAME write that decremented the count, never a follow-up pendingCounts() read.
  // Stopping the last pending agent inside a Solid batch() must still clear the spinner.
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

      // Stopping B (the LAST pending agent) inside a batch must still clear the spinner --
      // a follow-up read could observe the stale {B:1} map and wrongly keep it up.
      batch(() => s.stop('B'))
      expect(s.loading()).toBe(false)
      dispose()
    })
  })

  // [S6] pendingAxes: the optimistic-update suppression in useWorkspaceConnection is per-AXIS, not
  // just per-agent, so a server-initiated change to an UNRELATED axis on the same agent isn't held
  // back by a pending change on another axis.
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

        // The first change settles: model drops (only it touched model), effort stays (the second
        // change still has it in flight).
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
