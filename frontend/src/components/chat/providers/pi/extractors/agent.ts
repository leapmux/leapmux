import type { AgentRequestSource } from '../../../results/AgentRequestMessage'
import type { AgentResultSource } from '../../../results/agentResult'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../../rendererUtils'
import { PI_AGENT_TOOL } from '../protocol'
import { piExtractTool, piPairedRequest } from './toolCommon'

/** The pi-subagents extension emits these notes for incomplete runs. */
const STATUS_NOTES: Record<string, string> = {
  stopped: ' (STOPPED BY THE USER before completion — output is partial; the task was NOT finished)',
  aborted: ' (aborted — hit the turn limit before completion; output may be incomplete)',
  steered: ' (wrapped up at the turn limit — output may be partial)',
}

function counter(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

export function isPiAgentTool(toolName: string | undefined): boolean {
  return toolName === PI_TOOL.Agent || toolName === PI_TOOL.SubagentWorkflow || toolName === PI_AGENT_TOOL.GetResult || toolName === PI_AGENT_TOOL.Steer
}

export function piAgentRequest(payload: Record<string, unknown>, request?: ParsedMessageContent): AgentRequestSource {
  const tool = piExtractTool(payload)
  const args = piExtractTool(piPairedRequest(payload, request)?.parentObject)?.args ?? tool?.args ?? {}
  const details = tool?.result?.details ?? {}
  const metadata: NonNullable<AgentRequestSource['metadata']> = []
  if (tool?.toolName === PI_AGENT_TOOL.GetResult || tool?.toolName === PI_AGENT_TOOL.Steer) {
    const operation = tool.toolName === PI_AGENT_TOOL.GetResult ? 'Get agent result' : 'Send message'
    for (const [key, label] of [['wait', 'Wait'], ['verbose', 'Full conversation']]) {
      if (typeof args[key] === 'boolean')
        metadata.push({ label, value: args[key] ? 'Yes' : 'No' })
    }
    return { toolName: operation, description: `${operation}${pickString(args, 'agent_id') ? `: ${pickString(args, 'agent_id')}` : ''}`, prompt: pickString(args, 'message'), metadata }
  }
  for (const [key, label] of [['model', 'Model'], ['thinking', 'Thinking'], ['resume', 'Resume'], ['isolation', 'Isolation']]) {
    const value = pickString(args, key)
    if (value)
      metadata.push({ label, value })
  }
  if (counter(args.max_turns))
    metadata.push({ label: 'Maximum turns', value: formatNumber(args.max_turns) })
  return {
    toolName: PI_TOOL.Agent,
    description: pickString(args, 'description') || pickString(details, 'description'),
    agentType: pickString(args, 'subagent_type') || pickString(details, 'displayName') || pickString(details, 'subagentType'),
    prompt: pickString(args, 'prompt'),
    metadata,
  }
}

/** Remove a native summary only when its counters agree with the structured details. */
function agentReport(text: string, details: Record<string, unknown>): string {
  const separator = text.indexOf('\n\n')
  const header = separator >= 0 ? /^Agent completed in \d+\.\ds /.exec(text.slice(0, separator)) : null
  if (!header || !counter(details.toolUses))
    return text
  const tokens = pickString(details, 'tokens')
  const stats = `${details.toolUses} tool uses${tokens ? `, ${tokens}` : ''}`
  const status = pickString(details, 'status')
  const note = pickString(STATUS_NOTES, status)
  return text.slice(header[0].length, separator) === `(${stats})${note}.` && ['completed', 'stopped', 'aborted', 'steered'].includes(status)
    ? text.slice(separator + 2)
    : text
}

/** Native child status remains distinct from successful completion of the launch tool. */
export function piAgentResult(payload: Record<string, unknown>, request?: ParsedMessageContent): AgentResultSource {
  const tool = piExtractTool(payload)
  const result = tool?.result ?? tool?.partialResult
  const details = result?.details ?? {}
  if (tool?.toolName === PI_AGENT_TOOL.GetResult || tool?.toolName === PI_AGENT_TOOL.Steer) {
    const args = piExtractTool(piPairedRequest(payload, request)?.parentObject)?.args ?? tool.args
    return piAgentControlResult(tool.toolName, args, result?.text ?? '', tool.isError)
  }
  const nativeStatus = pickString(details, 'status')
  const failed = tool?.isError === true || nativeStatus === 'error'
  const state = failed ? 'failed' : nativeStatus
  const outcome: AgentResultSource['outcome'] = state === 'failed'
    ? 'failed'
    : state === 'completed'
      ? 'completed'
      : state === 'stopped'
        ? 'stopped'
        : ['running', 'background', 'queued'].includes(state) ? 'running' : 'unknown'
  const status = state === 'background'
    ? 'running'
    : state === 'aborted' || state === 'steered'
      ? 'partial'
      : state || 'returned a result'
  const metadata: AgentResultSource['metadata'] = []
  for (const [key, label] of [['agentId', 'Agent ID'], ['displayName', 'Agent type'], ['modelName', 'Model'], ['tokens', 'Tokens'], ['error', 'Error']]) {
    const value = pickString(details, key)
    if (value)
      metadata.push({ label, value })
  }
  if (!pickString(details, 'displayName') && pickString(details, 'subagentType'))
    metadata.push({ label: 'Agent type', value: pickString(details, 'subagentType') })
  for (const [key, label] of [['toolUses', 'Tool uses'], ['turnCount', 'Turns'], ['maxTurns', 'Maximum turns'], ['durationMs', 'Duration']]) {
    const value = details[key]
    if (counter(value))
      metadata.push({ label, value: key === 'durationMs' ? formatDuration(value) : formatNumber(value) })
  }
  if (Array.isArray(details.tags)) {
    const tags = details.tags.filter((value): value is string => typeof value === 'string' && value.trim() !== '')
    if (tags.length)
      metadata.push({ label: 'Options', value: tags.join(', ') })
  }
  const note = pickString(STATUS_NOTES, state)
  if (note)
    metadata.push({ label: 'Notice', value: note.trim().slice(1, -1) })
  const source = piAgentRequest(payload, request)
  if (!metadata.some(item => item.label === 'Agent type') && source.agentType)
    metadata.push({ label: 'Agent type', value: source.agentType })
  for (const item of source.metadata ?? []) {
    if (!metadata.some(existing => existing.label === item.label))
      metadata.push(item)
  }
  const body = agentReport(result?.text ?? '', details)
  return { description: source.description, agentId: pickString(details, 'agentId'), status, outcome, metadata, body }
}

/** The result tool omits structured details. Validate its complete header before extracting them. */
function retrievedAgentReport(text: string, expectedId: string): AgentResultSource | null {
  const separator = text.indexOf('\n\n')
  if (separator < 0)
    return null
  const lines = text.slice(0, separator).split('\n')
  if (lines.length !== 3 || !lines[0].startsWith('Agent: ') || !lines[2].startsWith('Description: '))
    return null
  const id = lines[0].slice('Agent: '.length)
  if (!id || (expectedId && expectedId !== id))
    return null
  const fields = lines[1].split(' | ')
  if (!fields[0]?.startsWith('Type: ') || !fields[1]?.startsWith('Status: ') || !/^Tool uses: \d+$/.test(fields[2] ?? ''))
    return null
  const stateText = fields[1].slice('Status: '.length)
  const state = stateText.split(' ', 1)[0]
  if (!['queued', 'running', 'completed', 'error', 'aborted', 'stopped', 'steered'].includes(state)
    || stateText !== state + pickString(STATUS_NOTES, state)) {
    return null
  }
  const metadata: AgentResultSource['metadata'] = [
    { label: 'Agent ID', value: id },
    { label: 'Agent type', value: fields[0].slice('Type: '.length) },
  ]
  for (const field of fields.slice(2)) {
    const divider = field.indexOf(': ')
    metadata.push(divider < 0 ? { label: 'Tokens', value: field } : { label: field.slice(0, divider), value: field.slice(divider + 2) })
  }
  const note = pickString(STATUS_NOTES, state)
  if (note)
    metadata.push({ label: 'Notice', value: note.trim().slice(1, -1) })
  return {
    description: lines[2].slice('Description: '.length),
    agentId: id,
    status: state === 'error' ? 'failed' : state === 'aborted' || state === 'steered' ? 'partial' : state,
    outcome: state === 'completed' ? 'completed' : state === 'error' ? 'failed' : state === 'stopped' ? 'stopped' : state === 'queued' || state === 'running' ? 'running' : 'unknown',
    metadata,
    body: text.slice(separator + 2),
  }
}

function piAgentControlResult(toolName: string, args: Record<string, unknown>, text: string, isError: boolean): AgentResultSource {
  const missing = /^Agent not found: "([^"\r\n]+)"\. It may have been cleaned up\.$/.exec(text)
  const id = pickString(args, 'agent_id') || missing?.[1] || ''
  const report = toolName === PI_AGENT_TOOL.GetResult ? retrievedAgentReport(text, id) : null
  if (report)
    return isError ? { ...report, outcome: 'failed', status: 'failed' } : report
  const unavailable = id && (text === `Agent not found: "${id}". It may have been cleaned up.`
    || (text.startsWith(`Agent "${id}" is not running (status: `) && text.endsWith('). Cannot steer a non-running agent.')))
  const failed = isError || !!unavailable || (toolName === PI_AGENT_TOOL.Steer && text.startsWith('Failed to steer agent: '))
  const sent = !failed && id && text.startsWith(`Steering message sent to agent ${id}. The agent will process it after its current tool execution.\nCurrent state: `)
  const queued = !failed && id && text === `Steering message queued for agent ${id}. It will be delivered once the session initializes.`
  return {
    description: '',
    agentId: id,
    status: failed ? 'failed' : sent ? 'received a message' : queued ? 'queued a message' : 'returned a result',
    outcome: failed ? 'failed' : sent || queued ? 'running' : 'unknown',
    metadata: id ? [{ label: 'Agent ID', value: id }] : [],
    body: text,
  }
}
