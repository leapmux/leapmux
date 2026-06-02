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

function loadFromStorage(agentId: string): AgentSessionInfo {
  return localStorageGet<AgentSessionInfo>(`${PREFIX_AGENT_SESSION}${agentId}`) ?? {}
}

function saveToStorage(agentId: string, info: AgentSessionInfo) {
  localStorageSet(`${PREFIX_AGENT_SESSION}${agentId}`, info)
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

  return {
    state,

    getInfo(agentId: string): AgentSessionInfo {
      if (!loaded.has(agentId)) {
        loaded.add(agentId)
        const stored = loadFromStorage(agentId)
        if (Object.keys(stored).length > 0) {
          setState('infoByAgent', agentId, stored)
        }
      }
      return state.infoByAgent[agentId] ?? {}
    },

    updateInfo(agentId: string, partial: Partial<AgentSessionInfo>) {
      if (!loaded.has(agentId)) {
        loaded.add(agentId)
        const stored = loadFromStorage(agentId)
        if (Object.keys(stored).length > 0) {
          setState('infoByAgent', agentId, stored)
        }
      }
      setState('infoByAgent', agentId, (prev = {}) => {
        const merged = { ...prev }
        let changed = false
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
            }
            continue
          }
          const current = (merged as Record<string, unknown>)[key]
          if (!shallowEqual(current, value)) {
            (merged as Record<string, unknown>)[key] = value
            changed = true
          }
        }
        if (!changed)
          return prev
        saveToStorage(agentId, merged)
        return merged
      })
    },

    clearContextUsage(agentId: string) {
      // Explicitly set properties to undefined so that Solid's store proxy
      // drops the tracked values. A functional updater that simply omits the
      // keys does NOT work because setState merges the returned object,
      // leaving the old properties on the proxy.
      setState('infoByAgent', agentId, 'contextUsage', undefined)
      setState('infoByAgent', agentId, 'totalCostUsd', undefined)
      const info = state.infoByAgent[agentId]
      if (info) {
        const { contextUsage: _, totalCostUsd: __, ...rest } = info
        saveToStorage(agentId, rest as AgentSessionInfo)
      }
    },
  }
}
