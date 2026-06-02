import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { compactionContextUsage, createAgentSessionStore } from '~/stores/agentSession.store'

describe('createAgentSessionStore', () => {
  beforeEach(() => {
    localStorage.clear()
  })

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
