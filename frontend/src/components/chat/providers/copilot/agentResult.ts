import type { AgentResultSource } from '../../results/agentResult'
import type { CopilotToolRow } from './toolPresentation'
import { pickString } from '~/lib/jsonPick'

/**
 * The subagent report one `task` tool call returned.
 *
 * A background launch returns as soon as the subagent starts, so its own result holds
 * the launch instruction rather than a report. The subagent's work is in its own
 * transcript either way, and its lifecycle is in the background-task row.
 */
export function copilotAgentResult(row: CopilotToolRow, output: string): AgentResultSource {
  const launched = pickString(row.input, 'mode') === 'background'
  const outcome = row.status === 'cancelled'
    ? 'stopped'
    : row.status === 'failed'
      ? 'failed'
      : launched ? 'running' : 'completed'
  const metadata: AgentResultSource['metadata'] = []
  for (const [label, key] of [['Agent', 'agent_type'], ['Model', 'model'], ['Effort', 'reasoning_effort']] as const) {
    const value = pickString(row.input, key)
    if (value)
      metadata.push({ label, value })
  }
  return {
    description: pickString(row.input, 'description') || pickString(row.input, 'name'),
    // The row key the worker opened for this subagent is its native agent id, which the
    // tool call itself never states. The registry row carries the title instead.
    agentId: '',
    status: outcome === 'running' ? 'launched asynchronously' : outcome,
    outcome,
    metadata,
    body: launched && outcome !== 'failed' ? pickString(row.input, 'prompt') : output,
    bodyLabel: launched && outcome !== 'failed' ? 'Prompt' : undefined,
  }
}
