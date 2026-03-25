import type { Accessor, Setter } from 'solid-js'
import { For } from 'solid-js'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { RefreshButton } from '~/components/common/RefreshButton'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { labelRow } from '~/styles/shared.css'

const allProviders = [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE]

interface AgentProviderSelectorProps {
  value: Accessor<AgentProvider>
  onChange: Setter<AgentProvider>
  availableProviders?: AgentProvider[]
  onRefresh?: () => void
}

export function AgentProviderSelector(props: AgentProviderSelectorProps) {
  const providers = () => props.availableProviders?.length
    ? props.availableProviders
    : allProviders

  return (
    <div>
      <div class={labelRow}>
        Agent Provider
        {props.onRefresh && (
          <RefreshButton onClick={() => props.onRefresh?.()} title="Refresh available providers" />
        )}
      </div>
      <select
        value={props.value()}
        onChange={e => props.onChange(Number(e.currentTarget.value) as AgentProvider)}
      >
        <For each={providers()}>
          {p => <option value={p}>{agentProviderLabel(p)}</option>}
        </For>
      </select>
    </div>
  )
}
