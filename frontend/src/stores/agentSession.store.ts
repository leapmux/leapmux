import { createStore } from 'solid-js/store'
import { safeGetJson, safeSetJson } from '~/lib/safeStorage'

export interface ContextUsageInfo {
  inputTokens: number
  cacheCreationInputTokens: number
  cacheReadInputTokens: number
  contextWindow?: number
}

export interface RateLimitInfo {
  status?: string              // "allowed" | "allowed_warning" | "exceeded" etc.
  resetsAt?: number            // Unix timestamp (seconds)
  rateLimitType?: string       // "five_hour" | "seven_day" etc.
  utilization?: number         // 0.0–1.0, current usage fraction
  surpassedThreshold?: number  // threshold that triggered warning (e.g. 0.75)
  overageStatus?: string       // "allowed" etc.
  overageResetsAt?: number     // Unix timestamp (seconds)
  isUsingOverage?: boolean
}

export interface AgentSessionInfo {
  totalCostUsd?: number
  contextUsage?: ContextUsageInfo
  rateLimits?: Record<string, RateLimitInfo>  // keyed by rateLimitType
}

const STORAGE_KEY_PREFIX = 'leapmux-agent-session-'

function loadFromStorage(agentId: string): AgentSessionInfo {
  return safeGetJson<AgentSessionInfo>(`${STORAGE_KEY_PREFIX}${agentId}`) ?? {}
}

function saveToStorage(agentId: string, info: AgentSessionInfo) {
  safeSetJson(`${STORAGE_KEY_PREFIX}${agentId}`, info)
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
        for (const [key, value] of Object.entries(partial)) {
          if (value !== undefined && value !== null) {
            if (key === 'rateLimits' && typeof value === 'object') {
              // Deep-merge rateLimits: preserve existing entries, update/add new ones
              merged.rateLimits = { ...merged.rateLimits, ...value as Record<string, RateLimitInfo> }
            } else {
              (merged as Record<string, unknown>)[key] = value
            }
          }
        }
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
