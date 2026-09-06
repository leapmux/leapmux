import type { BusyTab } from '~/components/shell/tabBusyProbe'
import type { Tab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createCloseFlow } from '~/components/shell/closeFlow'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'

interface TestCtx {
  tileId: string
}

function tab(id: string): Tab {
  return { type: TabType.AGENT, id, workspaceId: 'ws-1', tileId: 't1' }
}

describe('createCloseFlow', () => {
  it('request opens the dialog when the plan reports tabs', async () => createRoot(async (dispose) => {
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve: () => {}, finalize: () => {} }),
    })
    expect(flow.signal()).toBeNull()
    await flow.request({ tileId: 't1' })
    expect(flow.signal()).toEqual({ tileId: 't1' })
    expect(flow.busy()).toBe(false)
    flow.cancel()
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('request short-circuits to finalize when the plan reports no tabs', async () => createRoot(async (dispose) => {
    const finalize = vi.fn()
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [], preserve, finalize }),
    })
    await flow.request({ tileId: 't1' })
    expect(finalize).toHaveBeenCalledTimes(1)
    expect(preserve).not.toHaveBeenCalled()
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('primary fires preserve once and clears the signal', async () => createRoot(async (dispose) => {
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve, finalize: () => {} }),
    })
    await flow.request({ tileId: 't1' })
    flow.primary()
    expect(preserve).toHaveBeenCalledTimes(1)
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('primary bails when busy is true', async () => createRoot(async (dispose) => {
    const preserve = vi.fn()
    let observedBusy = false
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose: async () => {
        observedBusy = flow.busy()
        // Hold the loop so the test can observe busy=true.
        return new Promise<boolean>(() => {})
      },
      plan: () => ({ tabs: [tab('a1')], preserve, finalize: () => {} }),
    })
    await flow.request({ tileId: 't1' })
    void flow.closeAll()
    flow.primary()
    expect(preserve).not.toHaveBeenCalled()
    expect(flow.busy()).toBe(true)
    expect(observedBusy).toBe(true)
    dispose()
  }))

  it('primary is a no-op when no ctx is open', async () => createRoot(async (dispose) => {
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [], preserve, finalize: () => {} }),
    })
    flow.primary()
    expect(preserve).not.toHaveBeenCalled()
    dispose()
  }))

  it('scans for busy tabs BEFORE opening, so the dialog is complete on first paint', async () => createRoot(async (dispose) => {
    const order: string[] = []
    const busy = [{ tab: tab('a1'), title: 'a1', reason: { kind: 'agent-turn' as const, activeTasks: [] } }]
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => {
        order.push('probe')
        return busy
      },
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve: () => {}, finalize: () => {} }),
    })

    await flow.request({ tileId: 't1' })
    order.push('opened')

    // Opening first and patching the warning in would need a loading state AND
    // would leave a window where the user could confirm before it arrived.
    expect(order).toEqual(['probe', 'opened'])
    expect(flow.busyTabs()).toEqual(busy)
    dispose()
  }))

  it('does not scan an empty closeable', async () => createRoot(async (dispose) => {
    const probeBusy = vi.fn(async () => [])
    const finalize = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      probeBusy,
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [], preserve: () => {}, finalize }),
    })

    await flow.request({ tileId: 't1' })

    expect(probeBusy).not.toHaveBeenCalled()
    expect(finalize).toHaveBeenCalledTimes(1)
    dispose()
  }))

  it('closeAll suppresses the per-tab busy prompt, since the dialog already asked', async () => createRoot(async (dispose) => {
    const handleTabClose = vi.fn((_tab: Tab, _opts?: { skipBusyConfirm?: boolean }) => Promise.resolve(true))
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [],
      handleTabClose,
      plan: () => ({ tabs: [tab('a1'), tab('a2')], preserve: () => {}, finalize: () => {} }),
    })
    await flow.request({ tileId: 't1' })

    await flow.closeAll()

    // The per-tab WORKTREE prompt still runs: that one asks something this
    // dialog never covered.
    expect(handleTabClose.mock.calls.map(([, opts]) => opts)).toEqual([
      { skipBusyConfirm: true },
      { skipBusyConfirm: true },
    ])
    dispose()
  }))

  it('clears the busy list when the flow is cancelled', async () => createRoot(async (dispose) => {
    const flow = createCloseFlow<TestCtx>({
      probeBusy: async () => [{ tab: tab('a1'), title: 'a1', reason: { kind: 'agent-turn' as const, activeTasks: [] } }],
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve: () => {}, finalize: () => {} }),
    })
    await flow.request({ tileId: 't1' })
    expect(flow.busyTabs()).toHaveLength(1)

    flow.cancel()

    // A stale list would warn about the PREVIOUS closeable's work the next time
    // a dialog opened.
    expect(flow.busyTabs()).toEqual([])
    dispose()
  }))

  it('closeAll iterates tabs in order, calls finalize, and clears the signal', async () => {
    await createRoot(async (dispose) => {
      const handleTabClose = vi.fn().mockResolvedValue(true)
      const finalize = vi.fn()
      const tabs = [tab('a1'), tab('a2'), tab('a3')]
      const flow = createCloseFlow<TestCtx>({
        probeBusy: async () => [],
        handleTabClose,
        plan: () => ({ tabs, preserve: () => {}, finalize }),
      })
      await flow.request({ tileId: 't1' })
      await flow.closeAll()
      expect(handleTabClose).toHaveBeenCalledTimes(3)
      expect(handleTabClose.mock.calls.map(c => c[0].id)).toEqual(['a1', 'a2', 'a3'])
      expect(finalize).toHaveBeenCalledTimes(1)
      expect(flow.signal()).toBeNull()
      dispose()
    })
  })

  it('closeAll bails on first false return; signal stays open with busy:false; finalize never fires', async () => {
    await createRoot(async (dispose) => {
      const finalize = vi.fn()
      const handleTabClose = vi.fn()
        .mockResolvedValueOnce(true)
        .mockResolvedValueOnce(false)
      const flow = createCloseFlow<TestCtx>({
        probeBusy: async () => [],
        handleTabClose,
        plan: () => ({
          tabs: [tab('a1'), tab('a2'), tab('a3')],
          preserve: () => {},
          finalize,
        }),
      })
      await flow.request({ tileId: 't1' })
      await flow.closeAll()
      expect(handleTabClose).toHaveBeenCalledTimes(2)
      expect(finalize).not.toHaveBeenCalled()
      expect(flow.signal()).toEqual({ tileId: 't1' })
      expect(flow.busy()).toBe(false)
      dispose()
    })
  })

  it('closeAll sets busy:true while iterating', async () => {
    await createRoot(async (dispose) => {
      const observedBusy: boolean[] = []
      const flow = createCloseFlow<TestCtx>({
        probeBusy: async () => [],
        handleTabClose: async () => {
          observedBusy.push(flow.busy())
          return true
        },
        plan: () => ({
          tabs: [tab('a1'), tab('a2')],
          preserve: () => {},
          finalize: () => {},
        }),
      })
      await flow.request({ tileId: 't1' })
      await flow.closeAll()
      expect(observedBusy).toEqual([true, true])
      dispose()
    })
  })

  it('a second click during the scan starts no second scan, and cannot re-open a cancelled dialog', async () => {
    await createRoot(async (dispose) => {
      const pending: Array<(v: BusyTab[]) => void> = []
      const flow = createCloseFlow<TestCtx>({
        probeBusy: () => new Promise<BusyTab[]>(resolve => pending.push(resolve)),
        handleTabClose: () => Promise.resolve(true),
        plan: () => ({ tabs: [tab('a1')], preserve: () => {}, finalize: () => {} }),
      })

      // The close control carries no disabled state, and the scan made request()
      // async, so a double click reaches it twice.
      void flow.request({ tileId: 't1' })
      void flow.request({ tileId: 't1' })
      expect(pending).toHaveLength(1)

      pending[0]?.([])
      await Promise.resolve()
      expect(flow.signal()).toEqual({ tileId: 't1' })

      // The user dismisses it. A second scan resolving afterwards would re-open
      // the dialog over a decision the user already made.
      flow.cancel()
      pending[1]?.([])
      await Promise.resolve()
      expect(flow.signal()).toBeNull()
      dispose()
    })
  })

  it('closeAll bails when no ctx is open', async () => {
    await createRoot(async (dispose) => {
      const plan = vi.fn().mockReturnValue({ tabs: [], preserve: () => {}, finalize: () => {} })
      const flow = createCloseFlow<TestCtx>({
        probeBusy: async () => [],
        handleTabClose: () => Promise.resolve(true),
        plan,
      })
      await flow.closeAll()
      // closeAll without a request: nothing to iterate, plan was never called.
      expect(plan).not.toHaveBeenCalled()
      dispose()
    })
  })
})
