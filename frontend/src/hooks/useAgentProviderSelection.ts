import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal } from 'solid-js'
import { useMruProviders } from '~/hooks/useMruProviders'
import { getAvailableAgentProviders } from '~/lib/agentProviders'

interface UseAgentProviderSelectionResult {
  /**
   * Active provider selection. The accessor is `AgentProvider | undefined`
   * (NOT `AgentProvider`) because the signal is seeded from
   * `mruProviders()[0]`, which is `undefined` when the worker has zero
   * providers. Callers MUST gate the submit path on `!noProviders()` —
   * but the type now reflects that gate rather than lying about it, so a
   * forgetful caller hits a TS error at the proto field assignment
   * instead of silently sending `undefined` over the wire.
   */
  agentProvider: Accessor<AgentProvider | undefined>
  setAgentProvider: (v: AgentProvider) => void
  recordProviderUse: (provider: AgentProvider) => void
  available: Accessor<AgentProvider[]>
  noProviders: Accessor<boolean>
}

/**
 * Combines availability + MRU + selection into one signal:
 *   - `agentProvider` is seeded to the most-recently-used available
 *     provider on mount.
 *   - Re-seeds whenever the current choice falls out of availability
 *     (provider was removed at the worker, or the worker switched), so
 *     callers never have to defend against a stale selection.
 *   - Callers must gate the submit path on `!noProviders()`; when the
 *     worker has zero providers the signal is `undefined` and the
 *     accessor's type reflects that.
 *
 * Pass `availableProviders` directly from the dialog's prop (it may be
 * undefined while the parent is still loading); the composable
 * normalizes it via `getAvailableAgentProviders`.
 */
export function useAgentProviderSelection(
  availableProviders: Accessor<AgentProvider[] | undefined>,
): UseAgentProviderSelectionResult {
  const available = createMemo(() => getAvailableAgentProviders(availableProviders()))
  const { mruProviders, recordProviderUse } = useMruProviders(available, 1)
  const [agentProvider, setAgentProvider] = createSignal<AgentProvider | undefined>(mruProviders()[0])
  const noProviders = () => available().length === 0

  createEffect(() => {
    const best = mruProviders()[0]
    const current = agentProvider()
    if (best !== undefined && (current === undefined || !available().includes(current)))
      setAgentProvider(best)
  })

  return { agentProvider, setAgentProvider, recordProviderUse, available, noProviders }
}
