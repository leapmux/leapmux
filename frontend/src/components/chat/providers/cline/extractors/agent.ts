import type { AgentRequest, AgentResult, AgentRunStatus } from '../../../model/tools/agent'
import { CLINE_RUN_REASON } from '~/generated/contracts/cline-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { parseJSON } from './toolCommon'

/**
 * Cline's `spawn_agent` tool, and the tool of a configured agent of `.cline/agents/`
 * (`subagent_<name>_<hash>`).
 *
 *   spawn_agent:        {systemPrompt, task}  ->  {text, iterations, finishReason, usage}
 *   a configured agent: {prompt}              ->  the same result
 *
 * The call runs a child agent with the task and waits for it; its result is the
 * child's last answer. The worker keys the call's registry row by the call's own id,
 * which the request states so that the row points at it, and the child's transcript
 * hangs off that row.
 */

/** The title of a subagent whose task states nothing. The worker titles the row the same. */
export const CLINE_SUBAGENT_TITLE = 'Cline subagent'

/** The first non-blank line of a text, trimmed. */
function firstLine(text: string): string {
  return text.split('\n').map(line => line.trim()).find(line => line !== '') ?? ''
}

/** The launch one subagent call states: the task of `spawn_agent`, or the prompt of a configured agent. */
export function clineAgentRequest(args: Record<string, unknown>, callId: string): AgentRequest {
  const task = pickString(args, 'task') || pickString(args, 'prompt')
  return {
    description: firstLine(task) || CLINE_SUBAGENT_TITLE,
    prompt: task,
    promptFormat: 'markdown',
    registryKey: callId,
  }
}

/** How a subagent's run ended, from the reason Cline states. */
function runStatus(finishReason: string): AgentRunStatus {
  switch (finishReason) {
    case CLINE_RUN_REASON.Completed:
    case CLINE_RUN_REASON.MaxIterations:
    case '':
      return 'completed'
    case CLINE_RUN_REASON.Aborted:
      return 'stopped'
    default:
      return 'failed'
  }
}

/**
 * The words of a result: the run record's text, the value of a JSON string, or the
 * text itself. A text that reads as a JSON number or boolean is still the words the
 * call returned, not a record with no text.
 */
function reportText(output: unknown, value: unknown): string {
  if (isObject(value))
    return pickString(value, 'text')
  if (typeof value === 'string')
    return value
  return typeof output === 'string' ? output : ''
}

/**
 * The report one finished `spawn_agent` call returned, as the run it describes. Cline
 * states the result as an object live, and as its JSON text in a stored transcript.
 */
export function clineAgentResult(request: AgentRequest, output: unknown): AgentResult {
  const value = typeof output === 'string' ? parseJSON(output) ?? output : output
  const record = isObject(value) ? value : {}
  const text = reportText(output, value)
  return {
    agents: [{
      description: request.description,
      ...(request.registryKey !== undefined ? { registryKey: request.registryKey } : {}),
      agentId: '',
      outcome: runStatus(pickString(record, 'finishReason')),
      metadata: [],
      body: text,
    }],
  }
}
