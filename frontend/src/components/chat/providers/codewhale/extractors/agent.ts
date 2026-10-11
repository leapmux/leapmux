import type { AgentRequest, AgentRun, AgentRunStatus } from '../../../model/tools/agent'
import { CODEWHALE_AGENT_ACTION, CODEWHALE_AGENT_INPUT_FIELD, CODEWHALE_RESULT_FIELD } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

/**
 * How each subagent status word of the runtime reads as a run outcome.
 *
 * A child's own status (`subagent_status_name`) is one of `running`, `completed`,
 * `interrupted`, `failed`, `cancelled` and `budget_exhausted`. A status record states
 * the worker's word instead (`agent_worker_status_name`), which adds the live states
 * `queued`, `starting`, `waiting_for_user`, `model_wait` and `running_tool`. A word
 * from a later release reads as `unknown`, which states no state rather than a wrong
 * one.
 */
const AGENT_STATUS_OUTCOMES: ReadonlyMap<string, AgentRunStatus> = new Map<string, AgentRunStatus>([
  ['running', 'running'],
  ['queued', 'running'],
  ['starting', 'running'],
  ['waiting_for_user', 'running'],
  ['model_wait', 'running'],
  ['running_tool', 'running'],
  ['completed', 'completed'],
  ['failed', 'failed'],
  ['budget_exhausted', 'failed'],
  ['cancelled', 'stopped'],
  ['interrupted', 'stopped'],
])

/**
 * The launch or the management call one `agent` call states.
 *
 * The one tool starts, waits for, reads and stops subagents, and `action` says which.
 * A START states the child's name, its type and its prompt; every other action states
 * the ids it acts on, which the header reads as its description.
 */
export function codewhaleAgentRequest(args: Record<string, unknown>): AgentRequest {
  const action = pickString(args, CODEWHALE_AGENT_INPUT_FIELD.Action) || CODEWHALE_AGENT_ACTION.Start
  const agentType = pickString(args, CODEWHALE_AGENT_INPUT_FIELD.Type)
  const name = pickString(args, CODEWHALE_AGENT_INPUT_FIELD.Name)
  const target = pickString(args, CODEWHALE_AGENT_INPUT_FIELD.AgentID) || stringList(args[CODEWHALE_AGENT_INPUT_FIELD.AgentIDs]).join(', ')
  const description = action === CODEWHALE_AGENT_ACTION.Start
    ? name || agentType
    : [action, target].filter(Boolean).join(' ')
  return {
    description,
    ...(agentType ? { agentType } : {}),
    prompt: pickString(args, CODEWHALE_AGENT_INPUT_FIELD.Prompt),
  }
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string' && entry !== '') : []
}

/** One run a subagent record states, or null for a record that identifies no subagent. */
function agentRun(record: Record<string, unknown>, body: string, bodyLabel: string | undefined): AgentRun | null {
  const agentId = pickString(record, CODEWHALE_RESULT_FIELD.AgentID)
  if (!agentId)
    return null
  const status = pickString(record, CODEWHALE_RESULT_FIELD.Status)
  return {
    description: pickString(record, CODEWHALE_RESULT_FIELD.Name) || pickString(record, CODEWHALE_RESULT_FIELD.Role),
    agentId,
    ...(status ? { statusLabel: status.replaceAll('_', ' ') } : {}),
    outcome: AGENT_STATUS_OUTCOMES.get(status) ?? 'unknown',
    metadata: [{ label: 'Agent ID', value: agentId }],
    body,
    ...(bodyLabel !== undefined ? { bodyLabel } : {}),
  }
}

/**
 * The subagents one `agent` call answered about, or null for an answer that is not
 * the runtime's JSON record or that states no subagent.
 *
 * A START answers the one child it launched, `{name, agent_id, status, ...}`, and the
 * card then states the prompt it launched with. A WAIT answers the children that
 * settled, `{settled:[{agent_id, name, status}], running, note}`, and the card states
 * the note. The child's own transcript holds its work, so no card repeats it.
 */
export function codewhaleAgentRuns(text: string, request: AgentRequest): AgentRun[] | null {
  let document: unknown
  try {
    document = JSON.parse(text)
  }
  catch {
    return null
  }
  if (!isObject(document))
    return null
  const single = agentRun(document, request.prompt, request.prompt ? 'Prompt' : undefined)
  if (single)
    return [single]
  const settled = document[CODEWHALE_RESULT_FIELD.Settled]
  if (!Array.isArray(settled))
    return null
  const note = pickString(document, CODEWHALE_RESULT_FIELD.Note)
  const runs = settled.filter(isObject).flatMap((record) => {
    const run = agentRun(record, note, undefined)
    return run ? [run] : []
  })
  // A wait that settled nothing states only its note, which is no run to draw.
  return runs.length > 0 ? runs : null
}
