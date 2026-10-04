import type { ToolMetadataEntry } from '../../../model/toolMetadata'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { pickBoolean, pickNumber, pickString } from '~/lib/jsonPick'
import { grokRawOutput } from './results'

/**
 * The line that states the id of a subagent Grok started in the background.
 *
 * Grok answers a background launch with prose rather than a record, and this line is
 * the one fact in it that a later call refers to. The worker reads the same line, for
 * the same reason, to key the background task.
 */
const BACKGROUND_SUBAGENT_ID = /^subagent_id:\s*(\S+)\s*$/m

/** Whether a launch asked for a background run, under either spelling Grok sends. */
function grokRunsInBackground(input: Record<string, unknown>): boolean {
  return pickBoolean(input, 'background') === true || pickBoolean(input, 'run_in_background') === true
}

/** The entries one list states, keeping only the ones that carry a value. */
function presentEntries(entries: Array<[string, string]>): ToolMetadataEntry[] {
  return entries.filter(([, value]) => value !== '').map(([label, value]) => ({ label, value }))
}

/** The launch of one `spawn_subagent`, from its raw or its presented arguments. */
export function grokAgentRequest(input: Record<string, unknown>, title: string, toolCallId: string): AgentRequest {
  const metadata = presentEntries([
    ['Background', grokRunsInBackground(input) ? 'Yes' : ''],
    ['Model', pickString(input, 'model')],
    ['Isolation', pickString(input, 'isolation')],
    ['Working directory', pickString(input, 'cwd')],
    ['Resumes', pickString(input, 'resume_from') || pickString(input, 'task_id')],
  ])
  return {
    description: pickString(input, 'description') || title,
    prompt: pickString(input, 'prompt'),
    ...(metadata.length > 0 ? { metadata } : {}),
    // The worker keys the subagent's background task by the call that launched it.
    ...(toolCallId ? { registryKey: toolCallId } : {}),
  }
}

/**
 * The run one finished `spawn_subagent` reports.
 *
 * A foreground run answers `SubagentCompleted`, a record that states the id, the
 * counts and the report. A background launch answers prose that states the id alone,
 * and the subagent is still running then. A failed or stopped call keeps its own
 * outcome, whatever the output says.
 */
export function grokAgentRun(facts: ACPToolFacts, request: AgentRequest): AgentRun {
  const base = { description: request.description, ...(request.registryKey !== undefined ? { registryKey: request.registryKey } : {}) }
  if (facts.status === 'failed' || facts.status === 'cancelled')
    return { ...base, agentId: '', outcome: facts.status === 'failed' ? 'failed' : 'stopped', metadata: [], body: facts.text }
  const completed = grokRawOutput(facts.tool, 'SubagentCompleted')
  if (completed) {
    const duration = pickNumber(completed, 'duration_ms')
    const agentId = pickString(completed, 'subagent_id')
    return {
      ...base,
      agentId,
      outcome: 'completed',
      metadata: presentEntries([
        // The id the model passes as `resume_from` to continue this subagent.
        ['Agent ID', agentId],
        ['Type', pickString(completed, 'subagent_type')],
        ['Tool calls', String(pickNumber(completed, 'tool_calls') ?? '')],
        ['Turns', String(pickNumber(completed, 'turns') ?? '')],
        ['Duration', duration !== null ? `${(duration / 1000).toFixed(1)}s` : ''],
        ['Worktree', pickString(completed, 'worktree_path')],
      ]),
      body: pickString(completed, 'output'),
    }
  }
  const launched = BACKGROUND_SUBAGENT_ID.exec(facts.text)?.[1]
  if (launched)
    return { ...base, agentId: launched, outcome: 'running', statusLabel: 'running in the background', metadata: [{ label: 'Agent ID', value: launched }], body: facts.text }
  return { ...base, agentId: '', outcome: 'unknown', metadata: [], body: facts.text }
}

/** The launch of one `workflow` run: the script that it runs, or the saved workflow that it runs. */
export function grokWorkflowRequest(input: Record<string, unknown>, facts: ACPToolFacts): AgentRequest {
  const output = grokRawOutput(facts.tool, 'Workflow')
  const args = input.args
  const metadata = presentEntries([
    ['Script', pickString(output, 'script_path')],
    ['Arguments', args === undefined || args === null ? '' : typeof args === 'string' ? args : JSON.stringify(args)],
    ['Validate only', pickBoolean(input, 'validate_only') === true ? 'Yes' : ''],
  ])
  return {
    description: pickString(output, 'name') || 'Run workflow',
    prompt: pickString(input, 'source'),
    promptLabel: 'Script',
    promptFormat: 'pre',
    ...(metadata.length > 0 ? { metadata } : {}),
  }
}

/**
 * The run one finished `workflow` call started.
 *
 * The call returns as soon as the run STARTS: the run itself goes on, and its
 * subagents report under the run's own row. So a completed call states a run that is
 * still running, unless the call only validated the script.
 */
export function grokWorkflowRun(facts: ACPToolFacts, request: AgentRequest, input: Record<string, unknown>): AgentRun {
  if (facts.status === 'failed' || facts.status === 'cancelled')
    return { description: request.description, agentId: '', outcome: facts.status === 'failed' ? 'failed' : 'stopped', metadata: [], body: facts.text }
  const output = grokRawOutput(facts.tool, 'Workflow')
  const runId = pickString(output, 'run_id')
  return {
    description: request.description,
    agentId: runId,
    outcome: runId && pickBoolean(input, 'validate_only') !== true ? 'running' : 'completed',
    metadata: presentEntries([['Run ID', runId]]),
    body: pickString(output, 'message') || facts.text,
  }
}
