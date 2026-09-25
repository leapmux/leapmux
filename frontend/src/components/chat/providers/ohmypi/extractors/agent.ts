import type { RunStatus } from '../../../model/runStatus'
import type { ToolMetadataEntry } from '../../../model/toolMetadata'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import { OH_MY_PI_ASYNC_JOB_STATE } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../../rendererUtils'

/** One entry of a `task` call's `tasks` list. */
interface OhMyPiTaskSpec {
  name: string
  agent: string
  task: string
}

/**
 * The tasks one `task` call states.
 *
 * omp takes a list (`{context, tasks:[{name?, agent?, task}]}`) by default, and a
 * single task (`{agent?, task, name?}`) with `task.batch` off.
 */
function taskSpecs(args: Record<string, unknown>): OhMyPiTaskSpec[] {
  const read = (entry: Record<string, unknown>): OhMyPiTaskSpec => ({
    name: pickString(entry, 'name'),
    agent: pickString(entry, 'agent'),
    task: pickString(entry, 'task'),
  })
  if (Array.isArray(args.tasks))
    return args.tasks.filter(isObject).map(read).filter(spec => spec.task !== '' || spec.name !== '')
  const single = read(args)
  return single.task ? [single] : []
}

/** The first non-empty line of a text, trimmed. */
function firstLine(text: string): string {
  return text.split('\n').map(line => line.trim()).find(line => line !== '') ?? ''
}

/**
 * The launch one `task` call states: what the subagents were asked to do.
 *
 * One call can launch several subagents. The request then states how many, and the
 * prompt lists each task under its name, after the shared context.
 */
export function ohMyPiAgentRequest(args: Record<string, unknown>): AgentRequest {
  const specs = taskSpecs(args)
  const context = pickString(args, 'context').trim()
  const first = specs[0]
  const description = specs.length === 1 && first
    ? (first.name || firstLine(first.task) || 'Subagent')
    : specs.length > 1 ? `${specs.length} subagents` : 'Subagent'
  const agentTypes = [...new Set(specs.map(spec => spec.agent).filter(Boolean))]
  const sections = specs.length === 1 && first
    ? [first.task]
    : specs.map(spec => `### ${spec.name || firstLine(spec.task) || 'Task'}\n\n${spec.task}`)
  const prompt = [context, ...sections].filter(part => part.trim() !== '').join('\n\n')
  return {
    description,
    ...(agentTypes.length === 1 && agentTypes[0] ? { agentType: agentTypes[0] } : {}),
    prompt,
    promptFormat: 'markdown',
  }
}

/** The facts one subagent run states, as labelled rows. */
function runMetadata(entry: Record<string, unknown>): ToolMetadataEntry[] {
  const metadata: ToolMetadataEntry[] = []
  const id = pickString(entry, 'id')
  if (id)
    metadata.push({ label: 'Agent ID', value: id })
  const model = pickString(entry, 'resolvedModel')
  if (model)
    metadata.push({ label: 'Model', value: model })
  const durationMs = pickNumber(entry, 'durationMs', undefined)
  if (durationMs !== undefined)
    metadata.push({ label: 'Duration', value: formatDuration(durationMs) })
  const tokens = pickNumber(entry, 'tokens', undefined)
  if (tokens !== undefined && tokens > 0)
    metadata.push({ label: 'Tokens', value: formatNumber(tokens) })
  return metadata
}

/**
 * What one finished run's card draws below its facts, and the label above it.
 *
 * The run's output comes first. A run that printed no output states the first of
 * these that it holds:
 *
 * - The reason of a stop, for a run that stopped.
 * - Its error output.
 * - The error omp recorded, which is the only statement of a failed model call
 *   (`SingleResult`, `tui/src/tools/task.ts`).
 *
 * The reason and the error each take a label, so the reader does not take them for
 * output.
 */
function runBody(entry: Record<string, unknown>): Pick<AgentRun, 'body' | 'bodyLabel'> {
  const output = pickString(entry, 'output')
  if (output.trim())
    return { body: output }
  const stderr = pickString(entry, 'stderr').trim()
  const abortReason = entry.aborted === true ? pickString(entry, 'abortReason').trim() : ''
  if (abortReason)
    return { body: abortReason, bodyLabel: 'Abort reason' }
  if (stderr)
    return { body: stderr }
  const error = pickString(entry, 'error').trim()
  if (error)
    return { body: error, bodyLabel: 'Error' }
  return { body: output }
}

/** How one finished subagent run ended, from its exit code and its abort flag. */
function runOutcome(entry: Record<string, unknown>): RunStatus {
  if (entry.aborted === true)
    return 'stopped'
  const exitCode = pickNumber(entry, 'exitCode', undefined)
  if (exitCode === undefined)
    return 'unknown'
  return exitCode === 0 ? 'completed' : 'failed'
}

/** How one subagent the call left running stands, from omp's progress word. */
function progressOutcome(status: string): RunStatus {
  switch (status) {
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'aborted':
      return 'stopped'
    case 'pending':
    case 'running':
      return 'running'
    default:
      return 'unknown'
  }
}

/**
 * The subagent runs one finished `task` call states.
 *
 * A call that waited for its subagents states each run in `details.results`, with its
 * output. A call that left them running in the background -- omp's default -- states
 * each one in `details.progress` instead: the run goes on after the call ends, and its
 * registry row and its own transcript follow it.
 *
 * Returns an empty list for a result that states neither.
 */
export function ohMyPiAgentRuns(details: Record<string, unknown>, text: string): AgentRun[] {
  const results = Array.isArray(details.results) ? details.results.filter(isObject) : []
  if (results.length > 0) {
    return results.map((entry): AgentRun => {
      const assignment = pickString(entry, 'assignment') || pickString(entry, 'task')
      return {
        description: firstLine(assignment) || pickString(entry, 'id') || 'Subagent',
        agentId: pickString(entry, 'id'),
        outcome: runOutcome(entry),
        metadata: runMetadata(entry),
        ...runBody(entry),
      }
    })
  }
  const progress = Array.isArray(details.progress) ? details.progress.filter(isObject) : []
  const background = isObject(details.async) && pickString(details.async, 'state') === OH_MY_PI_ASYNC_JOB_STATE.Running
  return progress.map((entry): AgentRun => {
    const assignment = pickString(entry, 'assignment') || pickString(entry, 'task')
    return {
      description: firstLine(assignment) || pickString(entry, 'id') || 'Subagent',
      agentId: pickString(entry, 'id'),
      outcome: background ? 'running' : progressOutcome(pickString(entry, 'status')),
      ...(background ? { statusLabel: 'running in the background' } : {}),
      metadata: runMetadata(entry),
      body: background ? '' : text,
    }
  })
}
