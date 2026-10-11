import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { pickString } from '~/lib/jsonPick'
import { acpResultAvailable } from '../../acp/extractors/toolCall'

/** The prefix of the title that Kiro gives a subagent spawn: `Sub-agent: <name>`. */
const KIRO_SUBAGENT_TITLE_PREFIX = 'Sub-agent: '

/** The row title of a subagent whose spawn states no agent. */
const KIRO_SUBAGENT_FALLBACK = 'Subagent'

/**
 * The launch of one subagent.
 *
 * The spawn's arguments state the agent (`name`), the task (`prompt`) and the model's
 * reason for the spawn (`explanation`). The worker keys the subagent's registry row
 * by the call that spawned it.
 */
export function kiroAgentRequest(input: Record<string, unknown>, title: string, toolCallId: string): AgentRequest {
  const name = pickString(input, 'name') || (title.startsWith(KIRO_SUBAGENT_TITLE_PREFIX) ? title.slice(KIRO_SUBAGENT_TITLE_PREFIX.length) : '')
  const explanation = pickString(input, 'explanation').trim()
  return {
    description: name || KIRO_SUBAGENT_FALLBACK,
    ...(name ? { agentType: name } : {}),
    prompt: pickString(input, 'prompt'),
    ...(explanation ? { metadata: [{ label: 'Reason', value: explanation }] } : {}),
    ...(toolCallId ? { registryKey: toolCallId } : {}),
  }
}

/**
 * The run one finished spawn reports.
 *
 * Kiro states the subagent's answer as the spawn's raw output, a plain string. A
 * failed or stopped spawn keeps its own outcome, and its text is the reason. A spawn
 * that sent no answer before its turn ended states no outcome: the subagent may
 * still have run to its end.
 */
export function kiroAgentRun(facts: ACPToolFacts, request: AgentRequest, subtaskId: string): AgentRun {
  const base = { description: request.description, ...(request.registryKey !== undefined ? { registryKey: request.registryKey } : {}), agentId: subtaskId, metadata: [] }
  if (facts.status === 'failed' || facts.status === 'cancelled')
    return { ...base, outcome: facts.status === 'failed' ? 'failed' : 'stopped', body: facts.text }
  if (!acpResultAvailable(facts))
    return { ...base, outcome: 'unknown', body: '' }
  const answer = typeof facts.tool.rawOutput === 'string' ? facts.tool.rawOutput : facts.text
  return { ...base, outcome: 'completed', body: answer }
}
