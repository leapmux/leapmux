import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { localStorageLoad, localStorageStore, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
import { useTestStorage } from '~/test-support/persistentStorage'
import { compactionContextUsage, createAgentSessionStore } from './agentSession.store'

// The asynchronous storage tier has no in-memory mirror, so these round-trips
// need a database to round-trip through.
useTestStorage()

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

  // An agent id OF ITS OWN, because this is the one case that asserts the exact
  // stored document. A store defers its write behind the agent's hydrating read
  // and keeps no handle to cancel it, so a sibling case that disposed its root
  // while a read was outstanding still writes `agent-1` afterwards -- into
  // whichever database `useTestStorage` has installed by then.
  it('should persist after updateInfo', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-persist', { totalCostUsd: 1.5 })
      dispose()
    })
    // Polled: the store defers a write until the agent's stored row has been
    // read and merged, so the value lands an IndexedDB round trip later.
    await vi.waitFor(async () => {
      expect(await localStorageLoad<{ totalCostUsd: number }>(`${PREFIX_AGENT_SESSION}agent-persist`))
        .toEqual({ totalCostUsd: 1.5 })
    })
  })

  it('should load persisted info on first getInfo call', async () => {
    localStorageStore(`${PREFIX_AGENT_SESSION}agent-1`, { totalCostUsd: 3.0 })

    const dispose = createRoot(d => d)
    const store = createAgentSessionStore()
    // `getInfo` stays synchronous, so the FIRST read answers before the row
    // arrives -- it is the store's reactive update that carries it. Polling is
    // what a component does by re-rendering.
    store.getInfo('agent-1')
    await vi.waitFor(() => expect(store.getInfo('agent-1').totalCostUsd).toBe(3.0))
    dispose()
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

  it('should keep a false output minimum value', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.applyProgress('agent-1', { revision: 1, output: { bytes: 2048, minimum: false } })
      expect(store.getProgress('agent-1').output?.minimum).toBe(false)
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
  it('rejects a stale replay after a newer progress reset', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.applyProgress('ordered', { revision: 2 })
      store.applyProgress('ordered', { revision: 1, thinkingTokens: 500 })
      expect(store.getProgress('ordered')).toEqual({ revision: 2 })
      dispose()
    })
  })

  it('merges the thinking-token estimate without clobbering other keys', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-merge', { totalCostUsd: 0.5 })
      store.applyProgress('a-merge', { revision: 1, thinkingTokens: 230 })

      const info = store.getInfo('a-merge')
      expect(store.getProgress('a-merge').thinkingTokens).toBe(230)
      expect(info.totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('clearThinkingTokens drops only the estimate', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-clear', { totalCostUsd: 0.5 })
      store.applyProgress('a-clear', { revision: 1, thinkingTokens: 230 })

      store.clearThinkingTokens('a-clear')

      const info = store.getInfo('a-clear')
      expect(store.getProgress('a-clear').thinkingTokens).toBeUndefined()
      expect(info.totalCostUsd).toBe(0.5)
      dispose()
    })
  })

  it('clearOutputBytes drops both output fields and keeps other state', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-output', { totalCostUsd: 0.5 })
      store.applyProgress('a-output', {
        revision: 1,
        thinkingTokens: 20,
        output: { bytes: 2048, minimum: true },
      })

      store.clearOutputBytes('a-output')

      const info = store.getInfo('a-output')
      expect(store.getProgress('a-output').output).toBeUndefined()
      expect(store.getProgress('a-output').thinkingTokens).toBe(20)
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

  it('persists the cleared state so a reload does not resurrect the count', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-persist', { totalCostUsd: 0.5 })
      store.applyProgress('a-persist', { revision: 1, thinkingTokens: 230 })
      store.clearThinkingTokens('a-persist')
      dispose()
    })

    // What a fresh store rehydrates: the cleared estimate must not come back
    // while the surviving keys do.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-persist`))
        .toEqual({ totalCostUsd: 0.5 })
    })
  })

  it('writes nothing at all for an estimate-only update', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.applyProgress('a-eph-only', {
        revision: 1,
        thinkingTokens: 500,
        output: { bytes: 2048, minimum: true },
      })

      // The estimate is live in the reactive store...
      expect(store.getProgress('a-eph-only').thinkingTokens).toBe(500)
      expect(store.getProgress('a-eph-only').output?.bytes).toBe(2048)
      dispose()
    })
    // ...but an estimate-only update skips the write entirely (no entry is even
    // created), so live progress never causes a storage write.
    expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-eph-only`)).toBeUndefined()
  })

  it('never persists thinkingTokens, even when set alongside a persisted key', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // A single update carrying both a persisted key and the ephemeral
      // estimate: the estimate is live in memory but must never reach disk.
      store.updateInfo('a-ephemeral', { totalCostUsd: 0.5 })
      store.applyProgress('a-ephemeral', { revision: 1, thinkingTokens: 230 })
      expect(store.getProgress('a-ephemeral').thinkingTokens).toBe(230)
      dispose()
    })

    // Asserted against the STORED ROW rather than a second store: a store reads
    // an agent's row exactly once, so polling `getInfo` on one could never
    // observe a write that landed after that read.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-ephemeral`))
        .toEqual({ totalCostUsd: 0.5 })
    })
  })
})

describe('agentSessionStore clearContextUsage', () => {
  it('should clear contextUsage and totalCostUsd without affecting other fields', async () => {
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
      dispose()
    })
    await vi.waitFor(() => {
      const store = createAgentSessionStore()
      const info = store.getInfo('agent-1')
      expect(info.contextUsage).toBeUndefined()
      expect(info.totalCostUsd).toBeUndefined()
    })
    // The stored row must not carry them either.
    await vi.waitFor(async () => {
      const stored = await localStorageLoad<Record<string, unknown>>(`${PREFIX_AGENT_SESSION}agent-1`)
      expect(stored).toBeDefined()
      expect(stored?.contextUsage).toBeUndefined()
      expect(stored?.totalCostUsd).toBeUndefined()
    })
  })

  it('preserves sibling keys when clearing a not-yet-loaded agent', async () => {
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

    // Let the first store's write reach disk before the second store opens.
    // A reload -- which is what the second store stands for -- necessarily
    // happens after it, and without the wait the second store reads an empty
    // row and there is nothing for the clear to act on.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-unhydrated`)).toBeDefined()
    })

    // A fresh store has 'a-unhydrated' on disk but not in memory. Clearing
    // context usage before any getInfo/updateInfo must hydrate first, so it
    // does not persist a bare object over the stored rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.clearContextUsage('a-unhydrated')
      dispose()
    })

    // The stored row is what a later reload would read, and one equality states
    // the whole invariant: context usage and cost gone, rateLimits intact.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-unhydrated`))
        .toEqual({ rateLimits: { five_hour: { status: 'allowed' } } })
    })
  })

  // THE MERGE ORDER, which only an asynchronous read can get wrong. `ensureLoaded`
  // spreads `{...stored, ...prev}` -- `prev` LAST -- because a live update can
  // land while the read is in flight (a token count off the socket, a clear),
  // and the stored snapshot is older than any of them by construction. The
  // synchronous read this replaced could not race, so nothing pinned the order.
  it('lets a live update that landed during the read win over the stored row', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-racing', { totalCostUsd: 1, planFilePath: '/from-disk' })
      dispose()
    })
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-racing`)).toBeDefined()
    })

    await createRoot(async (dispose) => {
      const store = createAgentSessionStore()
      // Starts the read for this agent and answers from the still-empty entry.
      expect(store.getInfo('a-racing')).toEqual({})
      // The live update lands BEFORE the read resolves. With the spread the
      // other way round, the stored `/from-disk` would overwrite it a moment
      // later and the user would watch the value revert.
      store.updateInfo('a-racing', { planFilePath: '/from-the-socket' })
      expect(store.getInfo('a-racing').planFilePath).toBe('/from-the-socket')

      await vi.waitFor(() => {
        // The read has landed once a key only the stored row carries appears.
        expect(store.getInfo('a-racing').totalCostUsd).toBe(1)
      })
      expect(
        store.getInfo('a-racing').planFilePath,
        'the newer live value must survive the read it raced',
      ).toBe('/from-the-socket')
      dispose()
    })
  })

  it('is a no-op that preserves siblings when there is no context usage to clear', async () => {
    // Persist an agent that never carried contextUsage/cost, only rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-nocontext', { rateLimits: { five_hour: { status: 'allowed' } } })
      dispose()
    })

    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-nocontext`)).toBeDefined()
    })

    // Clearing context usage when neither key is present must short-circuit
    // without writing a bare object over the stored rateLimits.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.clearContextUsage('a-nocontext')
      dispose()
    })

    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-nocontext`))
        .toEqual({ rateLimits: { five_hour: { status: 'allowed' } } })
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
