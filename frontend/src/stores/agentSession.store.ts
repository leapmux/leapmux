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
  streamingType?: string // "plan" when streaming plan text, "" otherwise
  /**
   * Running estimate of the in-flight turn's thinking (reasoning) tokens.
   * Broadcast-only telemetry (never persisted as a timeline message); cleared
   * at each turn boundary so a stale per-turn count never lingers.
   */
  thinkingTokens?: number
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

// Keys that live in the reactive store for the UI but must never be persisted.
// thinkingTokens is a per-turn running estimate that streams many deltas per
// turn: persisting it would thrash storage with a write per
// delta AND rehydrate a stale count on reload (the indicator would show the
// pre-reload total until a fresh broadcast or turn-end clear corrects it).
// Stripped from every write, so the store mutates reactively but the value
// never reaches disk.
const EPHEMERAL_KEYS = ['thinkingTokens'] as const satisfies readonly (keyof AgentSessionInfo)[]

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

  // Track which agents have had their persisted info requested.
  const loaded = new Set<string>()
  // The in-flight read per agent, dropped once it settles. `persist` waits on
  // it; see there for why a write may not overtake a read.
  const loading = new Map<string, Promise<void>>()

  // Hydrate an agent's persisted info into the reactive store on first touch.
  // Every mutator must call this before reading/clearing keys: otherwise a
  // clear on a not-yet-loaded agent would see an empty in-memory entry and
  // could overwrite real persisted data (e.g. clearContextUsage saving a bare
  // `rest` over stored rateLimits/cost).
  //
  // `loaded` is marked SYNCHRONOUSLY and the read runs behind it, so `getInfo`
  // stays synchronous for the render path that calls it and the store notifies
  // when the row arrives, one microtask later. Marking it up front is what
  // keeps a burst of touches to one read.
  const ensureLoaded = (agentId: string) => {
    if (loaded.has(agentId))
      return
    loaded.add(agentId)
    const read = loadFromStorage(agentId).then((stored) => {
      if (Object.keys(stored).length === 0)
        return
      // `prev` LAST. A live update can land while the read is in flight -- a
      // token count from the socket, a clear -- and the stored snapshot is
      // older than any of them by construction, so it must fill gaps rather
      // than overwrite. The synchronous read this replaces could not race.
      setState('infoByAgent', agentId, (prev = {}) => ({ ...stored, ...prev }))
    })
    loading.set(agentId, read)
    void read.finally(() => {
      if (loading.get(agentId) === read)
        loading.delete(agentId)
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
    const read = loading.get(agentId)
    if (read === undefined)
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
        // thinkingTokens-only update mutates the reactive store but must not
        // hit storage -- it streams many deltas per turn, so writing on
        // each would thrash disk for a value that is never persisted anyway.
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
  }
}
