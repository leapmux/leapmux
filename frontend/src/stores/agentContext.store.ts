import { createStore } from 'solid-js/store'
import { safeGetJson, safeSetJson } from '~/lib/safeStorage'

export interface ContextUsageInfo {
  inputTokens: number
  cacheCreationInputTokens: number
  cacheReadInputTokens: number
  contextWindow?: number
}

export interface AgentContextInfo {
  totalCostUsd?: number
  contextUsage?: ContextUsageInfo
}

const STORAGE_KEY_PREFIX = 'leapmux-agent-context-'

function loadFromStorage(agentId: string): AgentContextInfo {
  return safeGetJson<AgentContextInfo>(`${STORAGE_KEY_PREFIX}${agentId}`) ?? {}
}

function saveToStorage(agentId: string, info: AgentContextInfo) {
  safeSetJson(`${STORAGE_KEY_PREFIX}${agentId}`, info)
}

interface AgentContextStoreState {
  infoByAgent: Record<string, AgentContextInfo>
}

export function createAgentContextStore() {
  const [state, setState] = createStore<AgentContextStoreState>({
    infoByAgent: {},
  })

  // Track which agents have been loaded from localStorage.
  const loaded = new Set<string>()

  return {
    state,

    getInfo(agentId: string): AgentContextInfo {
      if (!loaded.has(agentId)) {
        loaded.add(agentId)
        const stored = loadFromStorage(agentId)
        if (Object.keys(stored).length > 0) {
          setState('infoByAgent', agentId, stored)
        }
      }
      return state.infoByAgent[agentId] ?? {}
    },

    updateInfo(agentId: string, partial: Partial<AgentContextInfo>) {
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
            (merged as Record<string, unknown>)[key] = value
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
        saveToStorage(agentId, rest as AgentContextInfo)
      }
    },
  }
}
