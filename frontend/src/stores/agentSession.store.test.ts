import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { localStorageGet, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
import { createAgentSessionStore } from './agentSession.store'

describe('agentSessionStore thinkingTokens', () => {
  it('merges the thinking-token estimate without clobbering other keys', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-merge', { totalCostUsd: 0.5 })
      store.updateInfo('a-merge', { thinkingTokens: 230 })

      const info = store.getInfo('a-merge')
      expect(info.thinkingTokens).toBe(230)
      expect(info.totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('clearThinkingTokens drops only the estimate', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-clear', { totalCostUsd: 0.5, thinkingTokens: 230 })

      store.clearThinkingTokens('a-clear')

      const info = store.getInfo('a-clear')
      expect(info.thinkingTokens).toBeUndefined()
      expect(info.totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('clearThinkingTokens is a no-op when no estimate is set', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-noop', { totalCostUsd: 0.5 })

      expect(() => store.clearThinkingTokens('a-noop')).not.toThrow()
      expect(store.getInfo('a-noop').totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('clearThinkingTokens on an untouched agent is a safe no-op', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // The turn-end clear fires for every provider, including agents whose
      // session info was never loaded or never carried a thinking estimate.
      expect(() => store.clearThinkingTokens('a-untouched')).not.toThrow()
      expect(store.getInfo('a-untouched')).toEqual({})
      dispose()
    })
  })

  it('persists the cleared state so a reload does not resurrect the count', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-persist', { totalCostUsd: 0.5, thinkingTokens: 230 })
      store.clearThinkingTokens('a-persist')
      dispose()
    })

    // A fresh store rehydrates 'a-persist' from localStorage; the cleared
    // estimate must not come back while the surviving keys do.
    createRoot((dispose) => {
      const reloaded = createAgentSessionStore()
      const info = reloaded.getInfo('a-persist')
      expect(info.thinkingTokens).toBeUndefined()
      expect(info.totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('writes nothing to localStorage for an estimate-only update', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-eph-only', { thinkingTokens: 500 })

      // The estimate is live in the reactive store...
      expect(store.getInfo('a-eph-only').thinkingTokens).toBe(500)
      // ...but an estimate-only update skips the write entirely (no entry is
      // even created), so the per-delta stream never thrashes localStorage.
      expect(localStorageGet(`${PREFIX_AGENT_SESSION}a-eph-only`)).toBeUndefined()
      dispose()
    })
  })

  it('never persists thinkingTokens, even when set alongside a persisted key', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // A single update carrying both a persisted key and the ephemeral
      // estimate: the estimate is live in memory but must never reach disk.
      store.updateInfo('a-ephemeral', { totalCostUsd: 0.5, thinkingTokens: 230 })
      expect(store.getInfo('a-ephemeral').thinkingTokens).toBe(230)
      dispose()
    })

    createRoot((dispose) => {
      const reloaded = createAgentSessionStore()
      const info = reloaded.getInfo('a-ephemeral')
      expect(info.thinkingTokens).toBeUndefined() // ephemeral: never written
      expect(info.totalCostUsd).toBe(0.5) // the persisted sibling survived
      dispose()
    })
  })
})

describe('agentSessionStore clearContextUsage', () => {
  it('preserves sibling keys when clearing a not-yet-loaded agent', () => {
    // Persist an agent with context usage AND unrelated keys.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-unhydrated', {
        totalCostUsd: 1,
        contextUsage: {
          inputTokens: 10,
          cacheCreationInputTokens: 0,
          cacheReadInputTokens: 0,
        },
        rateLimits: { five_hour: { status: 'allowed' } },
      })
      dispose()
    })

    // A fresh store has 'a-unhydrated' on disk but not in memory. Clearing
    // context usage before any getInfo/updateInfo must hydrate first, so it
    // does not persist a bare object over the stored rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.clearContextUsage('a-unhydrated')
      dispose()
    })

    createRoot((dispose) => {
      const reloaded = createAgentSessionStore()
      const info = reloaded.getInfo('a-unhydrated')
      expect(info.contextUsage).toBeUndefined()
      expect(info.totalCostUsd).toBeUndefined() // clearContextUsage clears cost too
      expect(info.rateLimits).toEqual({ five_hour: { status: 'allowed' } }) // survived
      dispose()
    })
  })

  it('is a no-op that preserves siblings when there is no context usage to clear', () => {
    // Persist an agent that never carried contextUsage/cost, only rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-nocontext', { rateLimits: { five_hour: { status: 'allowed' } } })
      dispose()
    })

    // Clearing context usage when neither key is present must short-circuit
    // without writing a bare object over the stored rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.clearContextUsage('a-nocontext')
      dispose()
    })

    createRoot((dispose) => {
      const reloaded = createAgentSessionStore()
      expect(reloaded.getInfo('a-nocontext').rateLimits).toEqual({ five_hour: { status: 'allowed' } })
      dispose()
    })
  })
})
