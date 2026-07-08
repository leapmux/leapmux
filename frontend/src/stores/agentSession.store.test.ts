import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { localStorageGet, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
import { compactionContextUsage, createAgentSessionStore } from './agentSession.store'

// Isolate every test: unique-ID tests are unaffected (each does its own setup),
// while the reused-'agent-1' tests rely on a clean slate. Reload tests drive
// two roots inside one `it`, so the clear (per test, not per root) preserves
// their cross-reload persistence.
beforeEach(() => {
  localStorage.clear()
})

describe('createAgentSessionStore', () => {
  it('should return empty object for unknown agent on getInfo', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      expect(store.getInfo('unknown-agent')).toEqual({})
      dispose()
    })
  })

  it('should store data with updateInfo and retrieve with getInfo', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(1.5)
      dispose()
    })
  })

  it('should ignore null and undefined values in updateInfo', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 2.5 })
      store.updateInfo('agent-1', {
        totalCostUsd: undefined,
      } as Partial<{ totalCostUsd: number }>)
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(2.5)
      dispose()
    })
  })

  it('should merge with existing data without overwriting other fields', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      store.updateInfo('agent-1', {
        contextUsage: {
          inputTokens: 50000,
          cacheCreationInputTokens: 0,
          cacheReadInputTokens: 10000,
          contextWindow: 200000,
        },
      })
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(1.5)
      expect(info.contextUsage?.inputTokens).toBe(50000)
      dispose()
    })
  })

  it('should persist to localStorage after updateInfo', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      const raw = localStorage.getItem('leapmux:agent-session:agent-1')
      expect(raw).not.toBeNull()
      const wrapped = JSON.parse(raw!)
      expect(wrapped.v.totalCostUsd).toBe(1.5)
      expect(typeof wrapped.e).toBe('number')
      dispose()
    })
  })

  it('should load from localStorage on first getInfo call', () => {
    // Pre-seed localStorage with wrapped format before creating the store
    const preseeded = { totalCostUsd: 3.0 }
    localStorage.setItem('leapmux:agent-session:agent-1', JSON.stringify({ v: preseeded, e: Date.now() + 7 * 24 * 60 * 60 * 1000 }))

    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(3.0)
      dispose()
    })
  })

  it('should deep-merge rateLimits without overwriting other types', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', {
        rateLimits: { five_hour: { rateLimitType: 'five_hour', utilization: 0.5 } },
      })
      store.updateInfo('agent-1', {
        rateLimits: { seven_day: { rateLimitType: 'seven_day', utilization: 0.3 } },
      })
      const info = store.getInfo('agent-1')
      expect(info.rateLimits?.five_hour?.utilization).toBe(0.5)
      expect(info.rateLimits?.seven_day?.utilization).toBe(0.3)
      dispose()
    })
  })

  it('should update existing rateLimitType without affecting others', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', {
        rateLimits: {
          five_hour: { rateLimitType: 'five_hour', utilization: 0.5 },
          seven_day: { rateLimitType: 'seven_day', utilization: 0.3 },
        },
      })
      store.updateInfo('agent-1', {
        rateLimits: { five_hour: { rateLimitType: 'five_hour', utilization: 0.8 } },
      })
      const info = store.getInfo('agent-1')
      expect(info.rateLimits?.five_hour?.utilization).toBe(0.8)
      expect(info.rateLimits?.seven_day?.utilization).toBe(0.3)
      dispose()
    })
  })

  it('should merge rateLimits with existing totalCostUsd and contextUsage', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      store.updateInfo('agent-1', {
        rateLimits: { five_hour: { rateLimitType: 'five_hour', status: 'allowed_warning' } },
      })
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(1.5)
      expect(info.rateLimits?.five_hour?.status).toBe('allowed_warning')
      dispose()
    })
  })

  it('should store and retrieve planFilePath', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { planFilePath: '/home/user/.claude/plans/plan.md' })
      const info = store.getInfo('agent-1')
      expect(info.planFilePath).toBe('/home/user/.claude/plans/plan.md')
      dispose()
    })
  })

  it('should allow codexTurnId to be reset to an empty string', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { codexTurnId: 'turn-stale' })
      store.updateInfo('agent-1', { codexTurnId: '' })
      const info = store.getInfo('agent-1')
      expect(info.codexTurnId).toBe('')
      dispose()
    })
  })

  it('should update planFilePath without affecting other fields', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', { totalCostUsd: 2.0 })
      store.updateInfo('agent-1', { planFilePath: '/path/plan.md' })
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(2.0)
      expect(info.planFilePath).toBe('/path/plan.md')
      dispose()
    })
  })
})

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
  it('should clear contextUsage and totalCostUsd without affecting other fields', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', {
        totalCostUsd: 2.5,
        contextUsage: {
          inputTokens: 50000,
          cacheCreationInputTokens: 0,
          cacheReadInputTokens: 10000,
          contextWindow: 200000,
        },
      })
      store.clearContextUsage('agent-1')
      const info = store.getInfo('agent-1')
      expect(info.contextUsage).toBeUndefined()
      expect(info.totalCostUsd).toBeUndefined()
      // localStorage should also not contain contextUsage or totalCostUsd
      const raw = localStorage.getItem('leapmux:agent-session:agent-1')
      const wrapped = JSON.parse(raw!)
      expect(wrapped.v.contextUsage).toBeUndefined()
      expect(wrapped.v.totalCostUsd).toBeUndefined()
      dispose()
    })
  })

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

describe('compactionContextUsage', () => {
  it('zeroes the input/cache components and makes contextTokens authoritative', () => {
    expect(compactionContextUsage(12000, undefined)).toEqual({
      inputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
      contextTokens: 12000,
    })
  })

  it('preserves an existing context window so the percentage denominator survives', () => {
    const existing = { inputTokens: 50000, cacheCreationInputTokens: 40000, cacheReadInputTokens: 60000, contextWindow: 200000 }
    expect(compactionContextUsage(12000, existing)).toEqual({
      inputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
      contextTokens: 12000,
      contextWindow: 200000,
    })
  })

  it('omits contextWindow when none is known, rather than writing undefined', () => {
    const result = compactionContextUsage(8000, { inputTokens: 10, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 })
    expect('contextWindow' in result).toBe(false)
    expect(result.contextTokens).toBe(8000)
  })

  it('preserves a context window of 0 (present-but-falsy, not dropped)', () => {
    const result = compactionContextUsage(8000, { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, contextWindow: 0 })
    expect(result.contextWindow).toBe(0)
  })
})
