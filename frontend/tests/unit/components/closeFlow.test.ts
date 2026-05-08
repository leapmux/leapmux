import type { Tab } from '~/stores/tab.store'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createCloseFlow } from '~/components/shell/closeFlow'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

interface TestCtx {
  tileId: string
}

function tab(id: string): Tab {
  return { type: TabType.AGENT, id, tileId: 't1' }
}

describe('createCloseFlow', () => {
  it('request opens the dialog when the plan reports tabs', () => createRoot((dispose) => {
    const flow = createCloseFlow<TestCtx>({
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve: () => {}, finalize: () => {} }),
    })
    expect(flow.signal()).toBeNull()
    flow.request({ tileId: 't1' })
    expect(flow.signal()).toEqual({ tileId: 't1' })
    expect(flow.busy()).toBe(false)
    flow.cancel()
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('request short-circuits to finalize when the plan reports no tabs', () => createRoot((dispose) => {
    const finalize = vi.fn()
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [], preserve, finalize }),
    })
    flow.request({ tileId: 't1' })
    expect(finalize).toHaveBeenCalledTimes(1)
    expect(preserve).not.toHaveBeenCalled()
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('primary fires preserve once and clears the signal', () => createRoot((dispose) => {
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [tab('a1')], preserve, finalize: () => {} }),
    })
    flow.request({ tileId: 't1' })
    flow.primary()
    expect(preserve).toHaveBeenCalledTimes(1)
    expect(flow.signal()).toBeNull()
    dispose()
  }))

  it('primary bails when busy is true', () => createRoot((dispose) => {
    const preserve = vi.fn()
    let observedBusy = false
    const flow = createCloseFlow<TestCtx>({
      handleTabClose: async () => {
        observedBusy = flow.busy()
        // Hold the loop so the test can observe busy=true.
        return new Promise<boolean>(() => {})
      },
      plan: () => ({ tabs: [tab('a1')], preserve, finalize: () => {} }),
    })
    flow.request({ tileId: 't1' })
    void flow.closeAll()
    flow.primary()
    expect(preserve).not.toHaveBeenCalled()
    expect(flow.busy()).toBe(true)
    expect(observedBusy).toBe(true)
    dispose()
  }))

  it('primary is a no-op when no ctx is open', () => createRoot((dispose) => {
    const preserve = vi.fn()
    const flow = createCloseFlow<TestCtx>({
      handleTabClose: () => Promise.resolve(true),
      plan: () => ({ tabs: [], preserve, finalize: () => {} }),
    })
    flow.primary()
    expect(preserve).not.toHaveBeenCalled()
    dispose()
  }))

  it('closeAll iterates tabs in order, calls finalize, and clears the signal', async () => {
    await createRoot(async (dispose) => {
      const handleTabClose = vi.fn().mockResolvedValue(true)
      const finalize = vi.fn()
      const tabs = [tab('a1'), tab('a2'), tab('a3')]
      const flow = createCloseFlow<TestCtx>({
        handleTabClose,
        plan: () => ({ tabs, preserve: () => {}, finalize }),
      })
      flow.request({ tileId: 't1' })
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
        handleTabClose,
        plan: () => ({
          tabs: [tab('a1'), tab('a2'), tab('a3')],
          preserve: () => {},
          finalize,
        }),
      })
      flow.request({ tileId: 't1' })
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
      flow.request({ tileId: 't1' })
      await flow.closeAll()
      expect(observedBusy).toEqual([true, true])
      dispose()
    })
  })

  it('closeAll bails when no ctx is open', async () => {
    await createRoot(async (dispose) => {
      const plan = vi.fn().mockReturnValue({ tabs: [], preserve: () => {}, finalize: () => {} })
      const flow = createCloseFlow<TestCtx>({
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
