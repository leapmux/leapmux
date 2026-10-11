import type { RunStatus } from '../../../model/runStatus'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { TaskStatus } from '../../../model/tools/task'
import type { MiMoToolPart } from './toolCommon'
import { MIMO_ACTOR_ACTION, MIMO_ACTOR_OUTCOME, MIMO_ACTOR_STATUS } from '~/generated/contracts/mimo-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The operation an `actor` call carries, or undefined for input of another shape.
 *
 * MiMo states the operation as an object under `operation`. A model can send the
 * object as a JSON STRING, which MiMo refuses, so such a call ends in an error and
 * this reader states no operation for it.
 */
export function mimoActorOperation(input: Record<string, unknown>): Record<string, unknown> | undefined {
  const operation = input.operation
  return isObject(operation) ? operation : undefined
}

/** The action of an `actor` call: `spawn`, `run`, `send` and the rest. */
export function mimoActorAction(input: Record<string, unknown>): string {
  return pickString(mimoActorOperation(input), 'action')
}

/** True for an `actor` call that starts a subagent. */
export function mimoActorSpawns(input: Record<string, unknown>): boolean {
  const action = mimoActorAction(input)
  return action === MIMO_ACTOR_ACTION.Spawn || action === MIMO_ACTOR_ACTION.Run
}

/** The launch one spawning `actor` call asked for. */
export function mimoActorRequest(input: Record<string, unknown>): AgentRequest {
  const operation = mimoActorOperation(input)
  const agentType = pickString(operation, 'subagent_type')
  return {
    description: pickString(operation, 'description'),
    ...(agentType ? { agentType } : {}),
    prompt: pickString(operation, 'prompt'),
  }
}

/** The line of a background spawn's answer that states the actor. */
const BACKGROUND_STARTED = /^Background sub-session started\. actor_id: (\S+)$/m
/**
 * The line of a blocking run's answer that states the actor. It is the first line,
 * unless MiMo put a note in front of it.
 */
const RUN_IDENTITY = /^actor_id: (\S+) \(to give this subagent more work, `send` it another message\)$/m
/** The wrapper around a blocking run's report. */
const RUN_RESULT = /<actor_result status="([^"]*)"(?: summary="([^"]*)")?>\n([\s\S]*)\n<\/actor_result>/

/**
 * The note MiMo puts in front of a spawn's answer, or "" for none.
 *
 * MiMo runs a spawn whose `task_id` it cannot use without the task, and tells the
 * model why in a line in front of the answer (`note: task_id "T9" does not exist in
 * this session; ran ad-hoc. ...`). `index` is where the answer itself starts.
 */
function leadingNote(output: string, index: number): string {
  return output.slice(0, index).trim()
}

/**
 * The subagent one finished spawning call reports.
 *
 * A background spawn answers at once, with the actor's id, and the subagent reports
 * later in its own tab. A blocking run answers with the subagent's report, wrapped in
 * `<actor_result>`. The call's own action selects the form, because a report can hold
 * any text, the line of a background spawn included. A note that MiMo put in front of
 * either answer is stated beside the run. Null for an answer that is not in its
 * action's form: the caller then draws the words as they stand.
 */
export function mimoActorRun(part: MiMoToolPart): AgentRun | null {
  const request = mimoActorRequest(part.input)
  const actorId = pickString(part.metadata, 'actorId')
  const model = pickString(pickObject(part.metadata, 'model'), 'modelID')
  const metadata: AgentRun['metadata'] = []
  const withIdentity = (agentId: string, note: string): void => {
    if (agentId)
      metadata.push({ label: 'Agent ID', value: agentId })
    if (request.agentType)
      metadata.push({ label: 'Agent', value: request.agentType })
    if (model)
      metadata.push({ label: 'Model', value: model })
    if (note)
      metadata.push({ label: 'Note', value: note })
  }

  const action = mimoActorAction(part.input)
  const started = action === MIMO_ACTOR_ACTION.Spawn ? BACKGROUND_STARTED.exec(part.output) : null
  if (started) {
    const agentId = actorId || started[1] || ''
    withIdentity(agentId, leadingNote(part.output, started.index))
    return {
      description: request.description,
      agentId,
      statusLabel: 'launched in the background',
      outcome: 'running',
      metadata,
      body: request.prompt,
      bodyLabel: 'Prompt',
    }
  }

  if (action !== MIMO_ACTOR_ACTION.Run)
    return null
  const identity = RUN_IDENTITY.exec(part.output)
  const result = RUN_RESULT.exec(part.output)
  if (identity && result && identity.index < result.index) {
    const agentId = actorId || identity[1] || ''
    withIdentity(agentId, leadingNote(part.output, identity.index))
    const status = result[1] ?? ''
    const summary = result[2] ?? ''
    if (summary)
      metadata.push({ label: 'Summary', value: summary })
    return {
      description: request.description,
      agentId,
      // MiMo's own word: the subagent's reported status, or `timeout` and
      // `cancelled` for a run that did not report.
      ...(status ? { statusLabel: status } : {}),
      outcome: runOutcome(status),
      metadata,
      body: result[3] ?? '',
    }
  }
  return null
}

/**
 * How the subagent stands that an `actor` status, wait or cancel call asked about.
 *
 * MiMo states the subagent's status in `metadata.status`, and a wait adds the outcome
 * of the subagent's last turn in `metadata.lastOutcome`:
 *
 *   - `pending` and `running`: the subagent works. A wait that ended first
 *     (`timeout`) leaves it working too.
 *   - `cancelled`: a cancel stopped it.
 *   - `unknown`: MiMo found no such subagent, so the call reports a failure.
 *   - `idle`: the last turn ended, and its outcome decides. The status snapshot
 *     states no outcome, but it states the `error` of the last turn, which MiMo
 *     keeps for a failure alone.
 *
 * A status that this build does not know reads as completed, which claims no
 * failure that MiMo did not state. A workflow run states words of its own, and its
 * caller reads them.
 */
export function mimoActorTaskOutcome(part: MiMoToolPart): TaskStatus {
  switch (pickString(part.metadata, 'status')) {
    case MIMO_ACTOR_STATUS.Pending:
    case MIMO_ACTOR_STATUS.Running:
    case MIMO_ACTOR_STATUS.Timeout:
      return 'running'
    case MIMO_ACTOR_STATUS.Cancelled:
      return 'stopped'
    case MIMO_ACTOR_STATUS.Unknown:
      return 'failed'
    case MIMO_ACTOR_STATUS.Idle:
      return idleActorOutcome(part)
    default:
      return 'completed'
  }
}

/** The outcome of an idle subagent's last turn. */
function idleActorOutcome(part: MiMoToolPart): TaskStatus {
  const snapshot = parseSnapshot(part.output)
  const lastOutcome = pickString(part.metadata, 'lastOutcome') || pickString(snapshot, 'lastOutcome')
  switch (lastOutcome) {
    case MIMO_ACTOR_OUTCOME.Failure:
      return 'failed'
    case MIMO_ACTOR_OUTCOME.Cancelled:
      return 'stopped'
    case MIMO_ACTOR_OUTCOME.Success:
      return 'completed'
    default:
      return pickString(snapshot, 'error') ? 'failed' : 'completed'
  }
}

/** The JSON snapshot a status or a wait answers with, or undefined for other text. */
function parseSnapshot(output: string): Record<string, unknown> | undefined {
  try {
    const parsed: unknown = JSON.parse(output)
    return isObject(parsed) ? parsed : undefined
  }
  catch {
    return undefined
  }
}

/**
 * The outcome of a blocking run, from the status its wrapper states.
 *
 * The subagent reports `success`, `partial`, `blocked` or `failed` itself. MiMo writes
 * `cancelled` for a run that an abort stopped, and `timeout` for a run whose wait
 * ended while the subagent still worked -- which leaves it running.
 */
function runOutcome(status: string): RunStatus {
  switch (status) {
    case 'cancelled':
      return 'stopped'
    case 'failed':
      return 'failed'
    case 'timeout':
      return 'running'
    default:
      return 'completed'
  }
}
