import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { safeGetJson, safeSetJson } from '~/lib/safeStorage'

const STORAGE_KEY = 'leapmux:mru-agent-providers'

/** Read the ordered MRU provider list from localStorage. */
export function getMruProviders(): AgentProvider[] {
  return safeGetJson<AgentProvider[]>(STORAGE_KEY) ?? []
}

/** Move/add a provider to the front of the MRU list. */
export function touchMruProvider(provider: AgentProvider): void {
  const list = getMruProviders().filter(p => p !== provider)
  list.unshift(provider)
  safeSetJson(STORAGE_KEY, list)
}
