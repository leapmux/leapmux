import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { safeGetJson, safeSetJson } from '~/lib/safeStorage'
import { KEY_MRU_AGENT_PROVIDERS } from '~/lib/storageCleanup'

/** Read the ordered MRU provider list from localStorage. */
export function getMruProviders(): AgentProvider[] {
  return safeGetJson<AgentProvider[]>(KEY_MRU_AGENT_PROVIDERS) ?? []
}

/** Move/add a provider to the front of the MRU list. */
export function touchMruProvider(provider: AgentProvider): void {
  const list = getMruProviders().filter(p => p !== provider)
  list.unshift(provider)
  safeSetJson(KEY_MRU_AGENT_PROVIDERS, list)
}
