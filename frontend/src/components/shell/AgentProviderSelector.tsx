import type { Accessor, Setter } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'

interface AgentProviderSelectorProps {
  value: Accessor<AgentProvider>
  onChange: Setter<AgentProvider>
}

export function AgentProviderSelector(props: AgentProviderSelectorProps) {
  return (
    <div>
      <label>Agent Provider</label>
      <select
        value={props.value()}
        onChange={e => props.onChange(Number(e.currentTarget.value) as AgentProvider)}
      >
        <option value={AgentProvider.CLAUDE_CODE}>Claude Code</option>
        <option value={AgentProvider.CODEX}>Codex</option>
      </select>
    </div>
  )
}
