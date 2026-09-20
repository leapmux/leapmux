import type { AgentRun } from '../../../model/tools/agent'
import type { CopilotToolFacts } from './toolCall'
import { pickString } from '~/lib/jsonPick'

/**
 * The subagent report one `task` tool call returned.
 *
 * A background launch returns as soon as the subagent starts, so its own result holds
 * the launch instruction rather than a report. The subagent's work is in its own
 * transcript either way, and its lifecycle is in the background-task row.
 *
 * It reads the same `description`/`name` pair the `agent` request override reads, so
 * the request card and the report state one description.
 */
export function copilotAgentResult(facts: CopilotToolFacts): AgentRun {
  const args = facts.args
  const launched = pickString(args, 'mode') === 'background'
  const outcome = facts.status === 'cancelled'
    ? 'stopped'
    : facts.status === 'failed'
      ? 'failed'
      : launched ? 'running' : 'completed'
  const metadata: AgentRun['metadata'] = []
  for (const [label, key] of [['Agent', 'agent_type'], ['Model', 'model'], ['Effort', 'reasoning_effort']] as const) {
    const value = pickString(args, key)
    if (value)
      metadata.push({ label, value })
  }
  return {
    description: pickString(args, 'description') || pickString(args, 'name'),
    // The row key the worker opened for this subagent is its native agent id, which the
    // tool call itself never states. The registry row carries the title instead.
    agentId: '',
    ...(outcome === 'running' ? { statusLabel: 'launched asynchronously' } : {}),
    outcome,
    metadata,
    body: launched && outcome !== 'failed' ? pickString(args, 'prompt') : facts.output,
    ...(launched && outcome !== 'failed' ? { bodyLabel: 'Prompt' as const } : {}),
  }
}
