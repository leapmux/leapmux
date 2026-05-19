import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { flush } from '../../tests/unit/helpers/async'

const CC = AgentProvider.CLAUDE_CODE
const CODEX = AgentProvider.CODEX
const GEMINI = AgentProvider.GEMINI_CLI

// Mutable state the mocked mruAgentProviders backing store reads from.
// `useMruProviders` itself is real -- we want to exercise the real
// available/mru interaction without faking the composition layer.
let mockStored: AgentProvider[] = []
const touchSpy = vi.fn((p: AgentProvider) => {
  mockStored = [p, ...mockStored.filter(x => x !== p)]
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
      const sel = useAgentProviderSelection(() => [CC, CODEX, GEMINI])
      // CODEX is first in MRU and is in the available list.
      expect(sel.agentProvider()).toBe(CODEX)
      expect(sel.noProviders()).toBe(false)
      dispose()
    })
  })

  it('seeds from the first available provider when MRU is empty', () => {
    mockStored = []
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [GEMINI, CC])
      expect(sel.agentProvider()).toBe(GEMINI)
      dispose()
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
        setAvailable([CODEX, GEMINI])
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
        const [available, setAvailable] = createSignal<AgentProvider[]>([CC, CODEX, GEMINI])
        const sel = useAgentProviderSelection(available)
        expect(sel.agentProvider()).toBe(CC)

        sel.setAgentProvider(GEMINI)
        expect(sel.agentProvider()).toBe(GEMINI)

        // Availability changes but GEMINI is still in the list -- hook
        // must not stomp the user's explicit pick.
        setAvailable([CC, GEMINI])
        await flush()
        expect(sel.agentProvider()).toBe(GEMINI)
        dispose()
        resolve()
      })
    })
  })

  it('recordProviderUse delegates to the MRU store', () => {
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
    // Reference stability matters because `useMruProviders`'s memoized
    // `mruProviders` and the auto-re-seed effect both call `available()`
    // every reactive run; if it allocated a fresh array each time, the
    // downstream `.includes()` and ordering comparisons would force
    // unnecessary recomputations.
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
      const b = [CC, GEMINI]
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
      expect(third).toEqual([CC, GEMINI])
      dispose()
    })
  })

  it('noProviders is true exactly when availableProviders is an empty array', () => {
    mockStored = []
    createRoot((dispose) => {
      const [available, setAvailable] = createSignal<AgentProvider[] | undefined>([])
      const sel = useAgentProviderSelection(available)
      expect(sel.noProviders()).toBe(true)

      setAvailable([CC])
      expect(sel.noProviders()).toBe(false)

      // `undefined` is the "not loaded yet" signal -- the helper falls
      // back to DEFAULT_AGENT_PROVIDERS, so noProviders must be false.
      setAvailable(undefined)
      expect(sel.noProviders()).toBe(false)
      dispose()
    })
  })

  it('agentProvider() is undefined when available is empty (type matches runtime)', () => {
    // Regression: the signal used to be typed `AgentProvider` but seeded
    // from `mruProviders()[0]` which is undefined for an empty
    // availability list — the runtime value violated the declared type
    // and let consumers ship `undefined` as a non-nullable proto enum.
    // The signal is now typed `| undefined` so a forgetful caller hits
    // a TS error at the proto field assignment.
    mockStored = []
    createRoot((dispose) => {
      const sel = useAgentProviderSelection(() => [])
      expect(sel.noProviders()).toBe(true)
      expect(sel.agentProvider()).toBeUndefined()
      dispose()
    })
  })
})
