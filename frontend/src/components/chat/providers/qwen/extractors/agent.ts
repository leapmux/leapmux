import type { ToolMetadataEntry } from '../../../model/toolMetadata'
import type { AgentRequest, AgentRun, AgentRunStatus } from '../../../model/tools/agent'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/** The `rawOutput` type of a subagent run, which Qwen states on every `agent` result. */
const QWEN_TASK_EXECUTION = 'task_execution'

/**
 * The line that states the id of a subagent Qwen started in the background.
 *
 * Qwen answers a background launch with prose for the model, and this line is the
 * one fact in it that a later `send_message` or `task_stop` refers to.
 */
const BACKGROUND_TASK_ID = /^task_id:\s*(\S+)/m

/** The entries one list states, keeping only the ones that carry a value. */
function presentEntries(entries: Array<[string, string]>): ToolMetadataEntry[] {
  return entries.filter(([, value]) => value !== '').map(([label, value]) => ({ label, value }))
}

/** A count as text, or empty when the record states none. */
function countText(record: Record<string, unknown> | undefined | null, key: string): string {
  const value = pickNumber(record, key)
  return value !== null ? String(value) : ''
}

/**
 * How one run ended, in Qwen's own `status` word.
 *
 * `background` is a run that goes on after the call returned. A word this build
 * does not know states nothing, so the card claims no state for it.
 */
function qwenRunStatus(status: string): AgentRunStatus {
  switch (status) {
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'stopped'
    case 'background':
    case 'running':
      return 'running'
    default:
      return 'unknown'
  }
}

/** The launch of one `agent` call. */
export function qwenAgentRequest(input: Record<string, unknown>, toolCallId: string): AgentRequest {
  const agentType = pickString(input, 'subagent_type')
  const metadata = presentEntries([['Background', pickBoolean(input, 'run_in_background') === true ? 'Yes' : '']])
  return {
    description: pickString(input, 'description') || 'Subagent',
    prompt: pickString(input, 'prompt'),
    ...(agentType ? { agentType } : {}),
    ...(metadata.length > 0 ? { metadata } : {}),
    // The worker keys the subagent's background task by the call that launched it.
    ...(toolCallId ? { registryKey: toolCallId } : {}),
  }
}

/**
 * The run one finished `agent` call reports, from Qwen's `task_execution` record.
 *
 * A foreground run states its report under `result`. A background launch states the
 * status `background` and prose for the model, and the subagent is still running
 * then: its report arrives in its own transcript. A failed or stopped call keeps its
 * own outcome, whatever the record says.
 */
export function qwenAgentRun(facts: ACPToolFacts, request: AgentRequest): AgentRun {
  const base = { description: request.description, ...(request.registryKey !== undefined ? { registryKey: request.registryKey } : {}) }
  if (facts.status === 'failed' || facts.status === 'cancelled')
    return { ...base, agentId: '', outcome: facts.status === 'failed' ? 'failed' : 'stopped', metadata: [], body: facts.text }
  const raw = pickObject(facts.tool, 'rawOutput')
  if (!raw || pickString(raw, 'type') !== QWEN_TASK_EXECUTION)
    return { ...base, agentId: '', outcome: 'unknown', metadata: [], body: facts.text }
  const outcome = qwenRunStatus(pickString(raw, 'status'))
  if (outcome === 'running') {
    const taskId = BACKGROUND_TASK_ID.exec(facts.text)?.[1] ?? ''
    return {
      ...base,
      agentId: taskId,
      outcome,
      statusLabel: 'running in the background',
      metadata: presentEntries([['Task ID', taskId], ['Type', pickString(raw, 'subagentName')]]),
      body: '',
    }
  }
  const summary = pickObject(raw, 'executionSummary')
  const duration = pickNumber(summary, 'totalDurationMs')
  const terminateReason = pickString(raw, 'terminateReason')
  return {
    ...base,
    agentId: '',
    outcome,
    metadata: presentEntries([
      ['Type', pickString(raw, 'subagentName')],
      ['Rounds', countText(summary, 'rounds')],
      ['Tool calls', countText(summary, 'totalToolCalls')],
      ['Tokens', countText(summary, 'totalTokens')],
      ['Duration', duration !== null ? `${(duration / 1000).toFixed(1)}s` : ''],
      // `GOAL` is the ordinary end of a run, which the outcome already states.
      ['Ended by', terminateReason && terminateReason !== 'GOAL' ? terminateReason : ''],
    ]),
    body: pickString(raw, 'result') || facts.text,
  }
}

/** The launch of one `workflow` run: the script that it runs, the file that it reads, or the saved workflow that it runs. */
export function qwenWorkflowRequest(input: Record<string, unknown>): AgentRequest {
  const args = input.args
  const metadata = presentEntries([
    ['Script', pickString(input, 'scriptPath')],
    ['Saved workflow', pickString(input, 'name')],
    ['Previous run', pickString(input, 'resumeFromRunId')],
    ['Arguments', args === undefined || args === null ? '' : typeof args === 'string' ? args : JSON.stringify(args)],
  ])
  return {
    description: pickString(input, 'name') || pickString(input, 'scriptPath') || 'Run workflow',
    prompt: pickString(input, 'script'),
    promptLabel: 'Script',
    promptFormat: 'pre',
    ...(metadata.length > 0 ? { metadata } : {}),
  }
}

/** The fenced JSON record a workflow run answers with, or undefined when it is not one. */
function qwenWorkflowRecord(rawOutput: unknown): Record<string, unknown> | undefined {
  if (typeof rawOutput !== 'string')
    return undefined
  const fenced = /^```(?:json)?\n([\s\S]*)\n```\s*$/.exec(rawOutput.trim())
  try {
    const parsed: unknown = JSON.parse(fenced?.[1] ?? rawOutput)
    return isObject(parsed) ? parsed : undefined
  }
  catch {
    return undefined
  }
}

/**
 * The run one finished `workflow` call reports.
 *
 * Qwen runs the workflow inside the call, so a completed call states a finished run:
 * its id, and the result the script returned.
 */
export function qwenWorkflowRun(facts: ACPToolFacts, request: AgentRequest): AgentRun {
  if (facts.status === 'failed' || facts.status === 'cancelled')
    return { description: request.description, agentId: '', outcome: facts.status === 'failed' ? 'failed' : 'stopped', metadata: [], body: facts.text }
  const record = qwenWorkflowRecord(facts.tool.rawOutput)
  const result = record?.result
  const body = typeof result === 'string' ? result : result !== undefined && result !== null ? JSON.stringify(result, null, 2) : facts.text
  const phases = Array.isArray(record?.phases) ? record.phases.filter((phase): phase is string => typeof phase === 'string') : []
  return {
    description: request.description,
    agentId: pickString(record, 'runId'),
    outcome: 'completed',
    metadata: presentEntries([['Run ID', pickString(record, 'runId')], ['Phases', phases.join(', ')], ['Tokens', countText(pickObject(record, 'tokens'), 'spent')]]),
    body,
  }
}
