import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import * as browserStorage from '~/lib/browserStorage'
import { localStorageLoad, localStorageStore, PREFIX_AGENT_SESSION, setStorageAccountForTests } from '~/lib/browserStorage'
import { deferred } from '~/test-support/async'
import { useTestStorage } from '~/test-support/persistentStorage'
import { compactionContextUsage, createAgentSessionStore } from './agentSession.store'

// The asynchronous storage tier needs a database for these persistence checks.
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
      // fromEntries permits an explicit undefined value without a type assertion.
      // A present undefined value must not replace the stored value.
      store.updateInfo('agent-1', Object.fromEntries([['totalCostUsd', undefined]]))
      store.updateInfo('agent-1', Object.fromEntries([['totalCostUsd', null]]))
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

  // Use a separate agent ID for the exact stored-document assertion.
  // A disposed store can finish its pending write while the account stays unchanged.
  // That write can reach the next test's database for the same account.
  it('should persist after updateInfo', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-persist', { totalCostUsd: 1.5 })
      dispose()
    })
    // The write follows hydration through IndexedDB. Wait for the stored value.
    await vi.waitFor(async () => {
      expect(await localStorageLoad<{ totalCostUsd: number }>(`${PREFIX_AGENT_SESSION}agent-persist`))
        .toEqual({ totalCostUsd: 1.5 })
    })
  })

  it('should load persisted info on first getInfo call', async () => {
    localStorageStore(`${PREFIX_AGENT_SESSION}agent-1`, { totalCostUsd: 3.0 })

    const { store, dispose } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    // getInfo returns before the stored row arrives.
    // The reactive update supplies the hydrated value to the next render.
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

  it('replaces a full rate-limit snapshot and removes omitted tiers', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', {
        rateLimits: {
          five_hour: { rateLimitType: 'five_hour', status: 'allowed_warning' },
          seven_day: { rateLimitType: 'seven_day', status: 'allowed' },
        },
      })
      store.updateInfo('agent-1', {
        rateLimits: { seven_day: { rateLimitType: 'seven_day', status: 'allowed' } },
      }, { rateLimits: { mode: 'replace' } })

      expect(store.getInfo('agent-1').rateLimits).toEqual({
        seven_day: { rateLimitType: 'seven_day', status: 'allowed' },
      })
      dispose()
    })
  })

  it('removes one rate-limit entry when a full snapshot sends an empty tombstone', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('agent-1', {
        rateLimits: {
          account_block: { rateLimitType: 'workspace_owner_credits_depleted', status: 'exceeded' },
          five_hour: { rateLimitType: 'five_hour', utilization: 0.5 },
        },
      })
      store.updateInfo('agent-1', { rateLimits: {} }, {
        rateLimits: { mode: 'merge', deleteKeys: ['account_block'] },
      })

      expect(store.getInfo('agent-1').rateLimits).toEqual({
        five_hour: { rateLimitType: 'five_hour', utilization: 0.5 },
      })
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

  it('clears an explicit zero thinking count without changing other progress', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.applyProgress('zero-thinking', { revision: 1, thinkingTokens: 0, output: { bytes: 8, minimum: false } })
      store.clearThinkingTokens('zero-thinking')
      expect(store.getProgress('zero-thinking')).toEqual({ revision: 1, output: { bytes: 8, minimum: false } })
      dispose()
    })
  })

  it('clearThinkingTokens on an untouched agent is a safe no-op', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // Every provider clears at turn end, including an agent with no loaded estimate.
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

    // A fresh store must recover the surviving fields without the cleared estimate.
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

      // The reactive store retains the live estimate and output count.
      expect(store.getProgress('a-eph-only').thinkingTokens).toBe(500)
      expect(store.getProgress('a-eph-only').output?.bytes).toBe(2048)
      dispose()
    })
    // Live progress must create no stored entry.
    expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-eph-only`)).toBeUndefined()
  })

  it('never persists thinkingTokens, even when set alongside a persisted key', async () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // Separate updates supply persisted info and an ephemeral estimate.
      // The estimate must stay in memory when the info reaches storage.
      store.updateInfo('a-ephemeral', { totalCostUsd: 0.5 })
      store.applyProgress('a-ephemeral', { revision: 1, thinkingTokens: 230 })
      expect(store.getProgress('a-ephemeral').thinkingTokens).toBe(230)
      dispose()
    })

    // Read the stored row directly. A store hydrates an agent once per account.
    // Its getInfo cannot observe a later write from another store.
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
        planFilePath: '/clear-context-sibling',
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
    const { store, dispose } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    try {
      store.getInfo('agent-1')
      await vi.waitFor(() => {
        const info = store.getInfo('agent-1')
        expect(info.planFilePath).toBe('/clear-context-sibling')
        expect(info.contextUsage).toBeUndefined()
        expect(info.totalCostUsd).toBeUndefined()
      })
    }
    finally {
      dispose()
    }
    // The stored row must not carry them either.
    await vi.waitFor(async () => {
      const stored = await localStorageLoad<Record<string, unknown>>(`${PREFIX_AGENT_SESSION}agent-1`)
      expect(stored).toBeDefined()
      expect(stored?.contextUsage).toBeUndefined()
      expect(stored?.totalCostUsd).toBeUndefined()
    })
  })

  it('preserves sibling keys when clearing a not-yet-loaded agent', async () => {
    // Persist context usage beside unrelated fields.
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

    // A reload follows the first write. Wait for that write before the second store reads.
    // An earlier read could return an empty row and test no stored clear.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-unhydrated`)).toBeDefined()
    })

    // The new store knows no in-memory fields for this agent.
    // The clear must retain stored rate limits when it persists after hydration.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.clearContextUsage('a-unhydrated')
      dispose()
    })

    // A later reload reads this row. Context usage and cost are absent, and rate limits remain.
    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-unhydrated`))
        .toEqual({ rateLimits: { five_hour: { status: 'allowed' } } })
    })
  })

  // Hydration reads an older snapshot while a live update can change memory.
  // The merge must retain the live value for each field that the live update supplies.
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
      // Start the read while the in-memory entry stays empty.
      expect(store.getInfo('a-racing')).toEqual({})
      // Update before the read resolves. Reversed merge order would restore the older stored path.
      store.updateInfo('a-racing', { planFilePath: '/from-the-socket' })
      expect(store.getInfo('a-racing').planFilePath).toBe('/from-the-socket')

      await vi.waitFor(() => {
        // This field arrives only from the stored row and proves that hydration completed.
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
    // Persist rate limits without context usage or cost.
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('a-nocontext', { rateLimits: { five_hour: { status: 'allowed' } } })
      dispose()
    })

    await vi.waitFor(async () => {
      expect(await localStorageLoad(`${PREFIX_AGENT_SESSION}a-nocontext`)).toBeDefined()
    })

    // An absent clear must preserve the stored rate limits.
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

  it('drops the stated fill of the reading before the compaction', () => {
    // The percentage describes the old context.
    // Keeping it beside a post-compaction zero would display the old percentage again.
    const result = compactionContextUsage(0, { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, usagePercent: 88 })
    expect('usagePercent' in result).toBe(false)
    expect(result.contextTokens).toBe(0)
  })
})

describe('createAgentSessionStore account and hydration ownership', () => {
  it('clears the outgoing account info and progress before the next read', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('account-agent', { totalCostUsd: 99, planFilePath: '/old-account' })
      store.applyProgress('account-agent', { revision: 7, thinkingTokens: 40 })

      setStorageAccountForTests('replacement-account')

      expect(store.getInfo('account-agent')).toEqual({})
      expect(store.getProgress('account-agent')).toEqual({ revision: 0 })
      dispose()
    })
  })

  it('rejects the outgoing account hydration and its deferred write', async () => {
    const pending = deferred<{ totalCostUsd: number }>()
    const load = vi.spyOn(browserStorage, 'localStorageLoad').mockReturnValueOnce(pending.promise)
    const save = vi.spyOn(browserStorage, 'localStorageStore')
    const { store, dispose } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    try {
      store.getInfo('account-read-agent')
      store.updateInfo('account-read-agent', { totalCostUsd: 98 })

      setStorageAccountForTests('incoming-account')
      store.updateInfo('account-read-agent', { planFilePath: '/incoming-account' })
      pending.resolve({ totalCostUsd: 99 })

      await vi.waitFor(() => expect(save).toHaveBeenCalled())
      expect(store.getInfo('account-read-agent')).toEqual({ planFilePath: '/incoming-account' })
      for (const [key, value] of save.mock.calls) {
        if (key === `${PREFIX_AGENT_SESSION}account-read-agent`)
          expect(value).toEqual({ planFilePath: '/incoming-account' })
      }
    }
    finally {
      pending.resolve({ totalCostUsd: 99 })
      dispose()
      load.mockRestore()
      save.mockRestore()
    }
  })

  it('preserves a live reading that follows a clear before hydration ends', async () => {
    const pending = deferred<{ totalCostUsd: number, planFilePath: string }>()
    const load = vi.spyOn(browserStorage, 'localStorageLoad').mockReturnValueOnce(pending.promise)
    const save = vi.spyOn(browserStorage, 'localStorageStore')
    const { store, dispose } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    try {
      store.clearContextUsage('clear-read-agent')
      store.updateInfo('clear-read-agent', {
        totalCostUsd: 5,
        contextUsage: { inputTokens: 20, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 },
      })
      pending.resolve({ totalCostUsd: 1, planFilePath: '/retained-plan' })

      await vi.waitFor(() => expect(save).toHaveBeenCalled())
      expect(store.getInfo('clear-read-agent')).toEqual({
        totalCostUsd: 5,
        contextUsage: { inputTokens: 20, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 },
        planFilePath: '/retained-plan',
      })
    }
    finally {
      pending.resolve({ totalCostUsd: 1, planFilePath: '/retained-plan' })
      dispose()
      load.mockRestore()
      save.mockRestore()
    }
  })

  it('rejects an outgoing deferred write after the store owner closes', async () => {
    const pending = deferred<{ totalCostUsd: number }>()
    const load = vi.spyOn(browserStorage, 'localStorageLoad').mockReturnValueOnce(pending.promise)
    const save = vi.spyOn(browserStorage, 'localStorageStore')
    const { store: outgoing, dispose: disposeOutgoing } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    outgoing.updateInfo('closed-account-agent', { totalCostUsd: 98 })
    disposeOutgoing()
    setStorageAccountForTests('account-after-close')

    const { store: incoming, dispose: disposeIncoming } = createRoot(dispose => ({ store: createAgentSessionStore(), dispose }))
    try {
      incoming.updateInfo('closed-account-agent', { totalCostUsd: 7 })
      pending.resolve({ totalCostUsd: 99 })

      await vi.waitFor(() => expect(save).toHaveBeenCalledWith(`${PREFIX_AGENT_SESSION}closed-account-agent`, { totalCostUsd: 7 }))
      for (const [key, value] of save.mock.calls) {
        if (key === `${PREFIX_AGENT_SESSION}closed-account-agent`)
          expect(value).toEqual({ totalCostUsd: 7 })
      }
    }
    finally {
      pending.resolve({ totalCostUsd: 99 })
      disposeIncoming()
      load.mockRestore()
      save.mockRestore()
    }
  })
})

describe('createAgentSessionStore replay ownership', () => {
  it('keeps goal counter ownership separate from other counters and scalar fields', () => {
    createRoot((dispose) => {
      try {
        const store = createAgentSessionStore()
        store.beginReplay('goal-fields', 1n)
        store.claimGoalProgressWrite('goal-fields', 'tokensUsed', { phase: 'live' })
        expect(store.claimGoalProgressWrite('goal-fields', 'tokensUsed', { phase: 'replay', replayId: 1n })).toBe(false)
        expect(store.claimGoalProgressWrite('goal-fields', 'iterations', { phase: 'replay', replayId: 1n })).toBe(true)
        store.updateInfo('goal-fields', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: 1n } })
        expect(store.getInfo('goal-fields')).toEqual({ totalCostUsd: 2 })
      }
      finally {
        dispose()
      }
    })
  })

  it.each(['tokensUsed', 'tokenBudget', 'timeUsedSeconds', 'iterations'] as const)('protects an absent %s after an accepted live goal clear', (field) => {
    createRoot((dispose) => {
      try {
        const store = createAgentSessionStore()
        store.beginReplay('goal-clear', 1n)
        store.claimGoalProgressClear('goal-clear', { phase: 'live' })
        expect(store.claimGoalProgressWrite('goal-clear', field, { phase: 'replay', replayId: 1n, seq: 50n })).toBe(false)
        expect(store.claimGoalProgressWrite('goal-clear', field, { phase: 'live' })).toBe(true)
      }
      finally {
        dispose()
      }
    })
  })

  it('restores unclaimed goal counters only for a fresh active receipt', () => {
    createRoot((dispose) => {
      try {
        const store = createAgentSessionStore()
        store.beginReplay('goal-receipt', 1n)
        store.claimGoalProgressWrite('goal-receipt', 'tokensUsed', { phase: 'live' })
        store.retireReplay('goal-receipt', 1n)
        expect(store.claimGoalProgressWrite('goal-receipt', 'iterations', { phase: 'replay', replayId: 1n })).toBe(false)
        store.beginReplay('goal-receipt', 2n)
        expect(store.claimGoalProgressWrite('goal-receipt', 'tokensUsed', { phase: 'replay', replayId: 2n })).toBe(true)
        expect(store.claimGoalProgressWrite('goal-receipt', 'tokensUsed', { phase: 'replay', replayId: 1n, seq: 99n })).toBe(false)
      }
      finally {
        dispose()
      }
    })
  })

  it('preserves an ephemeral goal claim after a later sequenced live write', () => {
    createRoot((dispose) => {
      try {
        const store = createAgentSessionStore()
        store.beginReplay('goal-ephemeral', 1n)
        store.claimGoalProgressWrite('goal-ephemeral', 'tokensUsed', { phase: 'live' })
        store.claimGoalProgressWrite('goal-ephemeral', 'tokensUsed', { phase: 'live', seq: 30n })
        expect(store.claimGoalProgressWrite('goal-ephemeral', 'tokensUsed', { phase: 'replay', replayId: 1n, seq: 40n })).toBe(false)
      }
      finally {
        dispose()
      }
    })
  })

  it('clears goal receipt ownership when the storage account changes', () => {
    createRoot((dispose) => {
      try {
        const store = createAgentSessionStore()
        store.beginReplay('goal-account', 1n)
        store.claimGoalProgressClear('goal-account', { phase: 'live' })
        setStorageAccountForTests('goal-replacement-account')
        expect(store.claimGoalProgressWrite('goal-account', 'tokensUsed', { phase: 'replay', replayId: 1n })).toBe(false)
        store.beginReplay('goal-account', 2n)
        expect(store.claimGoalProgressWrite('goal-account', 'tokensUsed', { phase: 'replay', replayId: 2n })).toBe(true)
      }
      finally {
        dispose()
      }
    })
  })

  it('restores cold history independently for each scalar field', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const delivery = { phase: 'replay' as const, replayId: 0n, seq: 20n }
      store.updateInfo('cold-history', {
        totalCostUsd: 2,
        contextUsage: { inputTokens: 10, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 },
        rateLimits: { daily: { utilization: 0.5 } },
        planFilePath: '/history-plan',
      }, { delivery })
      expect(store.getInfo('cold-history')).toEqual({
        totalCostUsd: 2,
        contextUsage: { inputTokens: 10, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 },
        rateLimits: { daily: { utilization: 0.5 } },
        planFilePath: '/history-plan',
      })
      dispose()
    })
  })

  it.each([0n, 1n, (1n << 64n) - 3n])('replaces old ownership after request ID %s', (previousId) => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const agentId = `request-after-${previousId}`
      if (previousId > 0n)
        store.beginReplay(agentId, previousId)
      store.updateInfo(agentId, { totalCostUsd: 1 })
      store.beginReplay(agentId, previousId + 1n)
      store.updateInfo(agentId, { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: previousId + 1n, seq: 20n } })
      expect(store.getInfo(agentId).totalCostUsd).toBe(2)
      store.updateInfo(agentId, { totalCostUsd: 3 })
      store.beginReplay(agentId, previousId + 2n)
      store.updateInfo(agentId, { totalCostUsd: 4 }, { delivery: { phase: 'replay', replayId: previousId + 2n, seq: 40n } })
      expect(store.getInfo(agentId).totalCostUsd).toBe(4)
      dispose()
    })
  })

  it.each([19n, 20n, 21n])('compares history sequence %s with the exact live sequence', (historySeq) => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('ordered-history', 1n)
      store.updateInfo('ordered-history', { totalCostUsd: 3 }, { delivery: { phase: 'live', seq: 20n } })
      store.updateInfo('ordered-history', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: 1n, seq: historySeq } })
      expect(store.getInfo('ordered-history').totalCostUsd).toBe(historySeq > 20n ? 2 : 3)
      dispose()
    })
  })

  it('protects an ephemeral value through later durable updates in the same replay', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('ephemeral-history', 1n)
      store.updateInfo('ephemeral-history', { totalCostUsd: 3 })
      store.updateInfo('ephemeral-history', { totalCostUsd: 4 }, { delivery: { phase: 'live', seq: 30n } })
      store.updateInfo('ephemeral-history', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: 1n, seq: 40n } })
      expect(store.getInfo('ephemeral-history').totalCostUsd).toBe(4)
      dispose()
    })
  })

  it('protects a live zero without blocking another history field', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('zero-history', 1n)
      store.updateInfo('zero-history', { totalCostUsd: 0 })
      store.updateInfo('zero-history', { totalCostUsd: 2, planFilePath: '/history-plan' }, { delivery: { phase: 'replay', replayId: 1n, seq: 40n } })
      expect(store.getInfo('zero-history')).toEqual({ totalCostUsd: 0, planFilePath: '/history-plan' })
      dispose()
    })
  })

  it('protects an absent field that a live context clear owns', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('clear-history', 1n)
      store.clearContextUsage('clear-history')
      store.updateInfo('clear-history', {
        totalCostUsd: 2,
        contextUsage: { inputTokens: 10, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 },
        planFilePath: '/history-plan',
      }, { delivery: { phase: 'replay', replayId: 1n, seq: 40n } })
      expect(store.getInfo('clear-history')).toEqual({ planFilePath: '/history-plan' })
      dispose()
    })
  })

  it('rejects an old request and retires only the exact completed request', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('replaced-history', 1n)
      store.beginReplay('replaced-history', 2n)
      store.retireReplay('replaced-history', 1n)
      store.removeReplay('replaced-history', 1n)
      store.updateInfo('replaced-history', { totalCostUsd: 9 }, { delivery: { phase: 'replay', replayId: 1n, seq: 90n } })
      store.updateInfo('replaced-history', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: 2n, seq: 20n } })
      expect(store.getInfo('replaced-history').totalCostUsd).toBe(2)
      store.retireReplay('replaced-history', 2n)
      store.updateInfo('replaced-history', { totalCostUsd: 8 }, { delivery: { phase: 'replay', replayId: 2n, seq: 80n } })
      expect(store.getInfo('replaced-history').totalCostUsd).toBe(2)
      expect(store.acceptsReplay('replaced-history', 2n)).toBe(false)
      dispose()
    })
  })

  it('retains live ownership when the same requested replay repeats', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('same-history', 1n)
      store.updateInfo('same-history', { totalCostUsd: 3 })
      store.beginReplay('same-history', 1n)
      store.updateInfo('same-history', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId: 1n, seq: 20n } })
      expect(store.getInfo('same-history').totalCostUsd).toBe(3)
      dispose()
    })
  })

  it('does not replace a newer receipt with an older request', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.beginReplay('older-request', 2n)
      store.beginReplay('older-request', 1n)
      expect(store.acceptsReplay('older-request', 2n)).toBe(true)
      expect(store.acceptsReplay('older-request', 1n)).toBe(false)
      dispose()
    })
  })

  it('keeps request and sequence values above the safe numeric integer limit exact', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const replayId = 9007199254740993n
      const seq = 9007199254740995n
      store.beginReplay('large-history', replayId)
      store.updateInfo('large-history', { totalCostUsd: 3 }, { delivery: { phase: 'live', seq } })
      store.updateInfo('large-history', { totalCostUsd: 2 }, { delivery: { phase: 'replay', replayId, seq: seq - 1n } })
      expect(store.getInfo('large-history').totalCostUsd).toBe(3)
      store.updateInfo('large-history', { totalCostUsd: 4 }, { delivery: { phase: 'replay', replayId, seq: seq + 1n } })
      expect(store.getInfo('large-history').totalCostUsd).toBe(4)
      dispose()
    })
  })

  it.each([0n, -1n, 1n << 64n])('rejects invalid replay request ID %s', (replayId) => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      expect(() => store.beginReplay('invalid-history', replayId)).toThrow('positive uint64')
      expect(store.acceptsReplay('invalid-history', 0n)).toBe(true)
      dispose()
    })
  })

  it('accepts the largest uint64 request ID', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const replayId = (1n << 64n) - 1n
      store.beginReplay('largest-history', replayId)
      expect(store.acceptsReplay('largest-history', replayId)).toBe(true)
      dispose()
    })
  })
})
