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

  let prevDisplay: AgentProvider[] = []

  const mruProviders = (): AgentProvider[] => {
    // Track version for reactivity when recordProviderUse is called
    void version()
    const available = availableProviders()
    const stored = getMruProviders()
    const ideal: AgentProvider[] = []

    // Add MRU providers that are still available
    for (const p of stored) {
      if (ideal.length >= maxCount)
        break
      if (available.includes(p))
        ideal.push(p)
    }

    // Backfill from available providers if MRU doesn't fill all slots
    if (ideal.length < maxCount) {
      for (const p of available) {
        if (ideal.length >= maxCount)
          break
        if (!ideal.includes(p))
          ideal.push(p)
      }
    }

    // Stabilize display order: if the set of providers hasn't changed,
    // keep the previous ordering to avoid jarring button swaps.
    const idealSet = new Set(ideal)
    const prevSet = new Set(prevDisplay)
    if (
      idealSet.size === prevSet.size
      && [...idealSet].every(p => prevSet.has(p))
    ) {
      return prevDisplay
    }

    // Set membership changed: incumbents keep their slots, newcomers fill vacated slots.
    const newcomers = ideal.filter(p => !prevSet.has(p))
    let ni = 0
    const result: AgentProvider[] = []
    for (const p of prevDisplay) {
      if (idealSet.has(p)) {
        result.push(p)
      }
      else if (ni < newcomers.length) {
        result.push(newcomers[ni++])
      }
    }
    // Append remaining newcomers if prevDisplay was shorter
    while (ni < newcomers.length) {
      result.push(newcomers[ni++])
    }

    prevDisplay = result
    return result
  }

  const recordProviderUse = (provider: AgentProvider) => {
    touchMruProvider(provider)
    setVersion(v => v + 1)
  }

  return { mruProviders, recordProviderUse }
}
