import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { flush } from '~/test-support/async'

const CC = AgentProvider.CLAUDE_CODE
const CODEX = AgentProvider.CODEX
const CURSOR = AgentProvider.CURSOR

// Mutable state the mocked mruAgentProviders backing store reads from.
// `useMruProviders` itself is real -- we want to exercise the real
// available/mru interaction without faking the composition layer.
let mockStored: AgentProvider[] = []
const touchSpy = vi.fn((p: AgentProvider) => {
  mockStored = [
    p,
    ...mockStored.filter(x => x !== p),
  ]
})

vi.mock('~/lib/mruAgentProviders', () => ({
  getMruProviders: () => mockStored,
  touchMruProvider: (p: AgentProvider) => touchSpy(p),
}))

const { useAgentProviderSelection } = await import('./useAgentProviderSelection')

describe('useAgentProviderSelection', () => {
  it('seeds agentProvider from the most-recently-used available provider', () => {
    mockStored = [CODEX, CC]
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [CC, CODEX, CURSOR])
      // CODEX is first in MRU and is in the available list.
      expect(sel.agentProvider()).toBe(CODEX)
      expect(sel.noProviders()).toBe(false)
      dispose()
    })
  })

  it('seeds from the first available provider when MRU is empty', () => {
    mockStored = []
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [CURSOR, CC])
      expect(sel.agentProvider()).toBe(CURSOR)
      dispose()
    })
  })

  it('keeps the current provider when discovery adds a higher-priority option', async () => {
    mockStored = [CODEX]
    await createRoot(async (dispose) => {
      try {
        const [available, setAvailable] = createSignal<AgentProvider[]>([CC])
        const selection = useAgentProviderSelection(available)
        expect(selection.agentProvider()).toBe(CC)
        await flush()

        setAvailable([CC, CODEX])
        expect(selection.agentProvider()).toBe(CC)

        selection.setAgentProvider(CODEX)
        setAvailable([CC])
        expect(selection.agentProvider()).toBe(CC)
        setAvailable([CC, CODEX])
        expect(selection.agentProvider()).toBe(CC)
      }
      finally {
        dispose()
      }
    })
  })

  it('re-seeds when the current choice drops out of availability', async () => {
    mockStored = [CC]
    await new Promise<void>((resolve) => {
      createRoot(async (dispose) => {
        const [available, setAvailable] = createSignal<AgentProvider[]>([CC, CODEX])
        const sel = useAgentProviderSelection(available)
        expect(sel.agentProvider()).toBe(CC)

        // Worker stops offering CLAUDE_CODE. Hook must re-seed to a
        // still-available choice instead of holding a stale value the
        // submit path would later send to the worker.
        setAvailable([CODEX, CURSOR])
        await flush()
        expect(sel.agentProvider()).toBe(CODEX)
        dispose()
        resolve()
      })
    })
  })

  it('does not re-seed when the current choice is still available', async () => {
    mockStored = [CC, CODEX]
    await new Promise<void>((resolve) => {
      createRoot(async (dispose) => {
        const [available, setAvailable] = createSignal<AgentProvider[]>([CC, CODEX])
        const sel = useAgentProviderSelection(available)
        expect(sel.agentProvider()).toBe(CC)

        // Availability list shrinks but still contains the current choice.
        setAvailable([CC])
        await flush()
        expect(sel.agentProvider()).toBe(CC)
        dispose()
        resolve()
      })
    })
  })

  it('honors an explicit setAgentProvider override and keeps it through compatible availability changes', async () => {
    mockStored = [CC, CODEX]
    await new Promise<void>((resolve) => {
      createRoot(async (dispose) => {
        const [available, setAvailable] = createSignal<AgentProvider[]>([CC, CODEX, CURSOR])
        const sel = useAgentProviderSelection(available)
        expect(sel.agentProvider()).toBe(CC)

        sel.setAgentProvider(CURSOR)
        expect(sel.agentProvider()).toBe(CURSOR)

        // Availability changes but CURSOR is still in the list -- hook
        // must not stomp the user's explicit pick.
        setAvailable([CC, CURSOR])
        await flush()
        expect(sel.agentProvider()).toBe(CURSOR)
        dispose()
        resolve()
      })
    })
  })

  it('delegates recordProviderUse to the MRU store', () => {
    mockStored = []
    touchSpy.mockClear()
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [CC, CODEX])
      sel.recordProviderUse(CODEX)
      expect(touchSpy).toHaveBeenCalledWith(CODEX)
      dispose()
    })
  })

  it('available() is memoized: same source array → same reference across reads', () => {
    mockStored = []
    createRoot((dispose) => {
      const stable: AgentProvider[] = [CC, CODEX]
      const sel = useAgentProviderSelection(() => stable)
      const first = sel.available()
      const second = sel.available()
      expect(second).toBe(first)
      dispose()
    })
  })

  it('available() refreshes only when the source array changes', () => {
    mockStored = []
    createRoot((dispose) => {
      const a = [CC, CODEX]
      const b = [CC, CURSOR]
      const [source, setSource] = createSignal<AgentProvider[]>(a)
      const sel = useAgentProviderSelection(source)
      const first = sel.available()
      // Same array reference → memo short-circuits.
      const second = sel.available()
      expect(second).toBe(first)
      // New array → memo recomputes.
      setSource(b)
      const third = sel.available()
      expect(third).not.toBe(first)
      expect(third).toEqual([CC, CURSOR])
      dispose()
    })
  })

  it('blocks selection when the worker has no confirmed providers', () => {
    mockStored = []
    createRoot((dispose) => {
      const [available, setAvailable] = createSignal<AgentProvider[] | undefined>([])
      const sel = useAgentProviderSelection(available)
      expect(sel.noProviders()).toBe(true)

      setAvailable([CC])
      expect(sel.noProviders()).toBe(false)

      setAvailable(undefined)
      expect(sel.noProviders()).toBe(true)
      expect(sel.agentProvider()).toBeUndefined()
      dispose()
    })
  })

  it('leaves agentProvider() undefined when available is empty (type matches runtime)', () => {
    mockStored = []
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [])
      expect(sel.noProviders()).toBe(true)
      expect(sel.agentProvider()).toBeUndefined()
      dispose()
    })
  })
})
