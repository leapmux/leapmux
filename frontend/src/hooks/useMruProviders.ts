import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createSignal } from 'solid-js'
import { getMruProviders, touchMruProvider } from '~/lib/mruAgentProviders'

interface UseMruProvidersResult {
  mruProviders: Accessor<AgentProvider[]>
  recordProviderUse: (provider: AgentProvider) => void
}

export function useMruProviders(
  availableProviders: Accessor<AgentProvider[]>,
  maxCount = 2,
): UseMruProvidersResult {
  const [version, setVersion] = createSignal(0)

  const mruProviders = (): AgentProvider[] => {
    // Track version for reactivity when recordProviderUse is called
    void version()
    const available = availableProviders()
    const stored = getMruProviders()
    const result: AgentProvider[] = []

    // Add MRU providers that are still available
    for (const p of stored) {
      if (result.length >= maxCount)
        break
      if (available.includes(p))
        result.push(p)
    }

    // Backfill from available providers if MRU doesn't fill all slots
    if (result.length < maxCount) {
      for (const p of available) {
        if (result.length >= maxCount)
          break
        if (!result.includes(p))
          result.push(p)
      }
    }

    return result
  }

  const recordProviderUse = (provider: AgentProvider) => {
    touchMruProvider(provider)
    setVersion(v => v + 1)
  }

  return { mruProviders, recordProviderUse }
}
