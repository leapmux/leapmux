import { createStore } from 'solid-js/store'
import { localStorageGet, localStorageSet, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
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
  codexTurnId?: string // Codex active turn ID for interrupt
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
// turn: persisting it would thrash localStorage with a synchronous write per
// delta AND rehydrate a stale count on reload (the indicator would show the
// pre-reload total until a fresh broadcast or turn-end clear corrects it).
// Stripped from every write, so the store mutates reactively but the value
// never reaches disk.
const EPHEMERAL_KEYS = ['thinkingTokens'] as const satisfies readonly (keyof AgentSessionInfo)[]

function loadFromStorage(agentId: string): AgentSessionInfo {
  return localStorageGet<AgentSessionInfo>(`${PREFIX_AGENT_SESSION}${agentId}`) ?? {}
}

function saveToStorage(agentId: string, info: AgentSessionInfo) {
  const persisted = { ...info }
  for (const key of EPHEMERAL_KEYS)
    delete persisted[key]
  localStorageSet(`${PREFIX_AGENT_SESSION}${agentId}`, persisted)
}

interface AgentSessionStoreState {
  infoByAgent: Record<string, AgentSessionInfo>
}

export function createAgentSessionStore() {
  const [state, setState] = createStore<AgentSessionStoreState>({
    infoByAgent: {},
  })

  // Track which agents have been loaded from localStorage.
  const loaded = new Set<string>()

  // Hydrate an agent's persisted info into the reactive store on first touch.
  // Every mutator must call this before reading/clearing keys: otherwise a
  // clear on a not-yet-loaded agent would see an empty in-memory entry and
  // could overwrite real persisted data (e.g. clearContextUsage saving a bare
  // `rest` over stored rateLimits/cost).
  const ensureLoaded = (agentId: string) => {
    if (loaded.has(agentId))
      return
    loaded.add(agentId)
    const stored = loadFromStorage(agentId)
    if (Object.keys(stored).length > 0)
      setState('infoByAgent', agentId, stored)
  }

  return {
    state,

    getInfo(agentId: string): AgentSessionInfo {
      ensureLoaded(agentId)
      return state.infoByAgent[agentId] ?? {}
    },

    updateInfo(agentId: string, partial: Partial<AgentSessionInfo>) {
      ensureLoaded(agentId)
      setState('infoByAgent', agentId, (prev = {}) => {
        const merged = { ...prev }
        let changed = false
        // Tracks whether a *persisted* (non-ephemeral) key changed. A
        // thinkingTokens-only update mutates the reactive store but must not
        // hit localStorage -- it streams many deltas per turn, so writing on
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
        if (persistedChanged)
          saveToStorage(agentId, merged)
        return merged
      })
    },

    clearContextUsage(agentId: string) {
      // Hydrate first: without it, clearing a not-yet-loaded agent would build
      // `rest` from an empty in-memory entry and persist a bare object over the
      // agent's stored rateLimits/planFilePath/etc., silently wiping them.
      ensureLoaded(agentId)
      const info = state.infoByAgent[agentId]
      // Nothing tracked to drop and nothing on disk to scrub when neither key is
      // present -- skip the setState churn and the redundant localStorage write.
      if (!info || (info.contextUsage === undefined && info.totalCostUsd === undefined))
        return
      // Explicitly set properties to undefined so that Solid's store proxy
      // drops the tracked values. A functional updater that simply omits the
      // keys does NOT work because setState merges the returned object,
      // leaving the old properties on the proxy.
      setState('infoByAgent', agentId, 'contextUsage', undefined)
      setState('infoByAgent', agentId, 'totalCostUsd', undefined)
      const { contextUsage: _, totalCostUsd: __, ...rest } = info
      saveToStorage(agentId, rest as AgentSessionInfo)
    },

    clearThinkingTokens(agentId: string) {
      // Drop the per-turn thinking-token estimate at turn boundaries. Setting
      // the property to undefined (rather than omitting it from a merged
      // object) is required so Solid's store proxy actually removes the
      // tracked value — see clearContextUsage for the same rationale. No
      // localStorage write: thinkingTokens is an EPHEMERAL_KEY that is never
      // persisted, so there is nothing on disk to scrub.
      if (state.infoByAgent[agentId]?.thinkingTokens === undefined)
        return
      setState('infoByAgent', agentId, 'thinkingTokens', undefined)
    },
  }
}
