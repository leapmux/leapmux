import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { KEY_MRU_AGENT_PROVIDERS, localStorageGet, localStorageSet } from '~/lib/browserStorage'

/** Read the ordered MRU provider list from localStorage. */
export function getMruProviders(): AgentProvider[] {
  return localStorageGet<AgentProvider[]>(KEY_MRU_AGENT_PROVIDERS) ?? []
}

/** Move/add a provider to the front of the MRU list. */
export function touchMruProvider(provider: AgentProvider): void {
  const list = getMruProviders().filter(p => p !== provider)
  list.unshift(provider)
  localStorageSet(KEY_MRU_AGENT_PROVIDERS, list)
}
