import { createStore } from 'solid-js/store'
import { localStorageLoad, localStorageStore, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
import { shallowEqual } from '~/lib/shallowEqual'

export interface ContextUsageInfo {
  inputTokens: number
  cacheCreationInputTokens: number
  cacheReadInputTokens: number
  outputTokens?: number
  /** Authoritative provider-reported current context size, when available. */
  contextTokens?: number
  contextWindow?: number
}

export interface RateLimitInfo {
  status?: string // "allowed" | "allowed_warning" | "exceeded" etc.
  resetsAt?: number // Unix timestamp (seconds)
  rateLimitType?: string // "five_hour" | "seven_day" etc.
  utilization?: number // 0.0–1.0, current usage fraction
  surpassedThreshold?: number // threshold that triggered warning (e.g. 0.75)
  overageStatus?: string // "allowed" etc.
  overageResetsAt?: number // Unix timestamp (seconds)
  isUsingOverage?: boolean
}

export interface AgentSessionInfo {
  totalCostUsd?: number
  contextUsage?: ContextUsageInfo
  rateLimits?: Record<string, RateLimitInfo> // keyed by rateLimitType
  planFilePath?: string
  /**
   * Running estimate of the in-flight turn's thinking (reasoning) tokens.
   * Broadcast-only telemetry (never persisted as a timeline message); cleared
   * at each turn boundary so a stale per-turn count never lingers.
   */
  thinkingTokens?: number
  /** Bytes produced by live tool and process output. */
  outputBytes?: number
  /** True when the provider limits its live output and the count is a minimum. */
  outputBytesMinimum?: boolean
}

/**
 * Build the context-usage reading to apply after a completed compaction boundary.
 * The boundary reports only the post-compaction total (no input/cache breakdown),
 * so `contextTokens` becomes the authoritative size the grid reads and the
 * component fields reset to 0; a known context window is carried over from
 * `existing` so the percentage denominator survives. The next assistant message's
 * usage overwrites this transient reading. Exported so the connection handler and
 * its tests build the identical shape from one definition.
 */
export function compactionContextUsage(
  contextTokens: number,
  existing: ContextUsageInfo | undefined,
): ContextUsageInfo {
  return {
    inputTokens: 0,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    contextTokens,
    ...(existing?.contextWindow !== undefined ? { contextWindow: existing.contextWindow } : {}),
  }
}

// The Worker sends these live counters at a fixed maximum rate. Persisting
// them would cause needless storage writes and restore stale values after a
// reload. The store removes them from each storage write.
const EPHEMERAL_KEYS = [
  'thinkingTokens',
  'outputBytes',
  'outputBytesMinimum',
] as const satisfies readonly (keyof AgentSessionInfo)[]

async function loadFromStorage(agentId: string): Promise<AgentSessionInfo> {
  return (await localStorageLoad<AgentSessionInfo>(`${PREFIX_AGENT_SESSION}${agentId}`)) ?? {}
}

function saveToStorage(agentId: string, info: AgentSessionInfo) {
  const persisted = { ...info }
  for (const key of EPHEMERAL_KEYS)
    delete persisted[key]
  localStorageStore(`${PREFIX_AGENT_SESSION}${agentId}`, persisted)
}

interface AgentSessionStoreState {
  infoByAgent: Record<string, AgentSessionInfo>
}

export function createAgentSessionStore() {
  const [state, setState] = createStore<AgentSessionStoreState>({
    infoByAgent: {},
  })

  /**
   * Per agent: the in-flight persisted read, or `null` once it settled.
   *
   * ONE STRUCTURE, because presence and in-flight are two facts about one
   * lifecycle and they must agree. A key is present from the first touch, which
   * is what keeps a burst of touches to a single read; the value is the promise
   * `persist` and `clearContextUsage` wait on, and `null` afterwards. `null`
   * rather than a deleted key, so "settled" and "never requested" stay
   * distinguishable in the type rather than both reading as `undefined`.
   */
  const reads = new Map<string, Promise<void> | null>()

  // Hydrate an agent's persisted info into the reactive store on first touch.
  // Every mutator must call this before reading/clearing keys: otherwise a
  // clear on a not-yet-loaded agent would see an empty in-memory entry and
  // could overwrite real persisted data (e.g. clearContextUsage saving a bare
  // `rest` over stored rateLimits/cost).
  //
  // The key is registered SYNCHRONOUSLY and the read runs behind it, so `getInfo`
  // stays synchronous for the render path that calls it and the store notifies
  // when the row arrives, one microtask later. Registering it up front is what
  // keeps a burst of touches to one read.
  const ensureLoaded = (agentId: string) => {
    if (reads.has(agentId))
      return
    const read = loadFromStorage(agentId).then((stored) => {
      if (Object.keys(stored).length === 0)
        return
      // `prev` LAST. A live update can land while the read is in flight -- a
      // token count from the socket, a clear -- and the stored snapshot is
      // older than any of them by construction, so it must fill gaps rather
      // than overwrite. The synchronous read this replaces could not race.
      setState('infoByAgent', agentId, (prev = {}) => ({ ...stored, ...prev }))
    // A failed read is a MISS: the agent simply has no stored info, which is
    // already a defined outcome here. Without this the rejection would also
    // travel into `whenLoaded`, whose `read.then(body)` would then DROP the
    // body -- losing the write it was waiting to make, and leaving the next
    // `whenLoaded` to run synchronously against an entry that never hydrated.
    // That is exactly the clobber `ensureLoaded` exists to prevent.
    }).catch(() => {})
    // Set AFTER the chain is built and still in the same turn: `loadFromStorage`
    // is an `async` function, so it cannot throw synchronously and runs no user
    // code before its first await. Nothing can re-enter `ensureLoaded` between
    // the `has` above and this line.
    reads.set(agentId, read)
    void read.finally(() => {
      // Only this read's own entry: a newer one would own the key by then.
      if (reads.get(agentId) === read)
        reads.set(agentId, null)
    })
  }

  /**
   * Run `body` once `agentId`'s stored row has been merged into the store,
   * immediately when there is nothing in flight.
   *
   * WAITING ON THE READ IS THE WHOLE POINT, for reads and writes alike. A
   * mutator runs synchronously against the in-memory entry, and until the read
   * lands that entry is empty -- so `clearContextUsage` on an agent nobody has
   * touched yet would either find nothing to clear and drop the clear, or write
   * a bare object OVER the stored rateLimits and cost. That is exactly the
   * clobber `ensureLoaded` was written to prevent, and making the read
   * asynchronous re-opened it.
   */
  const whenLoaded = (agentId: string, body: () => void) => {
    // `null` (settled) and absent (never requested) both run the body now, which
    // is why one falsy test answers for both.
    const read = reads.get(agentId)
    if (!read)
      body()
    else
      void read.then(body)
  }

  /**
   * Persist whatever the store holds for `agentId`, once its row has merged in.
   *
   * It reads the state at WRITE time rather than taking a snapshot, so what it
   * stores is the merged value the read produced, not the pre-merge one the
   * mutator saw.
   */
  const persist = (agentId: string) => {
    whenLoaded(agentId, () => {
      const info = state.infoByAgent[agentId]
      if (info !== undefined)
        saveToStorage(agentId, info)
    })
  }

  return {
    state,

    getInfo(agentId: string): AgentSessionInfo {
      ensureLoaded(agentId)
      return state.infoByAgent[agentId] ?? {}
    },

    updateInfo(agentId: string, partial: Partial<AgentSessionInfo>) {
      ensureLoaded(agentId)
      // Set by the updater, acted on AFTER it: `persist` reads the store, and
      // inside the updater the store still holds the pre-update value.
      let shouldPersist = false
      setState('infoByAgent', agentId, (prev = {}) => {
        const merged = { ...prev }
        let changed = false
        // Tracks whether a *persisted* (non-ephemeral) key changed. A
        // A live-counter update mutates the reactive store but must not reach
        // storage.
        let persistedChanged = false
        for (const [key, value] of Object.entries(partial)) {
          if (value === undefined || value === null)
            continue
          if (key === 'rateLimits' && typeof value === 'object') {
            // Deep-merge rateLimits: preserve existing entries, update/add new ones.
            const incoming = value as Record<string, RateLimitInfo>
            const existing = merged.rateLimits ?? {}
            const next = { ...existing }
            let rlChanged = false
            for (const [rlKey, rlInfo] of Object.entries(incoming)) {
              if (!shallowEqual(existing[rlKey], rlInfo)) {
                next[rlKey] = rlInfo
                rlChanged = true
              }
            }
            if (rlChanged) {
              merged.rateLimits = next
              changed = true
              persistedChanged = true
            }
            continue
          }
          const current = (merged as Record<string, unknown>)[key]
          if (!shallowEqual(current, value)) {
            (merged as Record<string, unknown>)[key] = value
            changed = true
            if (!(EPHEMERAL_KEYS as readonly string[]).includes(key))
              persistedChanged = true
          }
        }
        if (!changed)
          return prev
        shouldPersist = persistedChanged
        return merged
      })
      if (shouldPersist)
        persist(agentId)
    },

    clearContextUsage(agentId: string) {
      // Hydrate first, and act only once the row has landed: without that,
      // clearing a not-yet-loaded agent either finds an empty in-memory entry
      // and drops the clear, or persists a bare object over the agent's stored
      // rateLimits/planFilePath/etc. and silently wipes them.
      ensureLoaded(agentId)
      whenLoaded(agentId, () => {
        const info = state.infoByAgent[agentId]
        // Nothing tracked to drop and nothing on disk to scrub when neither key
        // is present -- skip the setState churn and the redundant write.
        if (!info || (info.contextUsage === undefined && info.totalCostUsd === undefined))
          return
        // Explicitly set properties to undefined so that Solid's store proxy
        // drops the tracked values. A functional updater that simply omits the
        // keys does NOT work because setState merges the returned object,
        // leaving the old properties on the proxy.
        setState('infoByAgent', agentId, 'contextUsage', undefined)
        setState('infoByAgent', agentId, 'totalCostUsd', undefined)
        persist(agentId)
      })
    },

    clearThinkingTokens(agentId: string) {
      // Drop the per-turn thinking-token estimate at turn boundaries. Setting
      // the property to undefined (rather than omitting it from a merged
      // object) is required so Solid's store proxy actually removes the
      // tracked value — see clearContextUsage for the same rationale. No
      // storage write: thinkingTokens is an EPHEMERAL_KEY that is never
      // persisted, so there is nothing on disk to scrub.
      if (state.infoByAgent[agentId]?.thinkingTokens === undefined)
        return
      setState('infoByAgent', agentId, 'thinkingTokens', undefined)
    },

    clearOutputBytes(agentId: string) {
      if (state.infoByAgent[agentId]?.outputBytes === undefined
        && state.infoByAgent[agentId]?.outputBytesMinimum === undefined) {
        return
      }
      setState('infoByAgent', agentId, 'outputBytes', undefined)
      setState('infoByAgent', agentId, 'outputBytesMinimum', undefined)
    },
  }
}
