import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import { createAgentContextStore } from '~/stores/agentContext.store'

describe('createAgentContextStore', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('should return empty object for unknown agent on getInfo', () => {
    createRoot((dispose) => {
      const store = createAgentContextStore()
      expect(store.getInfo('unknown-agent')).toEqual({})
      dispose()
    })
  })

  it('should store data with updateInfo and retrieve with getInfo', () => {
    createRoot((dispose) => {
      const store = createAgentContextStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(1.5)
      dispose()
    })
  })

  it('should ignore null and undefined values in updateInfo', () => {
    createRoot((dispose) => {
      const store = createAgentContextStore()
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
      const store = createAgentContextStore()
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
      const store = createAgentContextStore()
      store.updateInfo('agent-1', { totalCostUsd: 1.5 })
      const raw = localStorage.getItem('leapmux-agent-context-agent-1')
      expect(raw).not.toBeNull()
      const parsed = JSON.parse(raw!)
      expect(parsed.totalCostUsd).toBe(1.5)
      dispose()
    })
  })

  it('should clear contextUsage and totalCostUsd without affecting other fields', () => {
    createRoot((dispose) => {
      const store = createAgentContextStore()
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
      const raw = localStorage.getItem('leapmux-agent-context-agent-1')
      const parsed = JSON.parse(raw!)
      expect(parsed.contextUsage).toBeUndefined()
      expect(parsed.totalCostUsd).toBeUndefined()
      dispose()
    })
  })

  it('should load from localStorage on first getInfo call', () => {
    // Pre-seed localStorage before creating the store
    const preseeded = { totalCostUsd: 3.0 }
    localStorage.setItem('leapmux-agent-context-agent-1', JSON.stringify(preseeded))

    createRoot((dispose) => {
      const store = createAgentContextStore()
      const info = store.getInfo('agent-1')
      expect(info.totalCostUsd).toBe(3.0)
      dispose()
    })
  })
})
