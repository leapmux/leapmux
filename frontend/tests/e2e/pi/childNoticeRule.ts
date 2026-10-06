import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { hasNativeToolResult, nativeToolCallArguments, nativeToolResult } from '../helpers/nativeToolResult'
import { encodeNativeXmlText } from '../helpers/nativeXml'
import { waitForNewestModelRequest } from '../helpers/newestModelRequest'
import { requireNonemptyText } from '../helpers/requiredText'

export interface PiChildLaunch {
  spawnCallId: string
  description: string
  childId: string
  outputFile?: string
}

interface PiNoticeOptions {
  name: string
  spawnCallId: string
  description: string
  report: string
  reply: string
  once?: boolean
}

export interface PiWorkflowNoticeOptions {
  name: string
  taskId: string
  callId: string
  workflowName: string
  scriptPath?: string
  reports: readonly string[]
  reply: string
  once?: boolean
}

/** Refuse a notice input that holds no text. */
function requireNotice(value: string, field: string): void {
  requireNonemptyText(value, 'The Pi notification', field)
}

/** Read only the actual result of one Agent call from the isolated OpenAI-compatible API. */
export function piChildLaunch(request: MockModelRequestRecord, spawnCallId: string): PiChildLaunch | null {
  requireNotice(spawnCallId, 'spawnCallId')
  if (request.protocol !== 'openai-chat-completions' || !hasNativeToolResult(request, spawnCallId))
    return null
  const call = nativeToolCallArguments(request, spawnCallId)
  if (call.name !== 'Agent')
    throw new Error('The Pi child receipt contains no unique actual Agent call.')
  const input = call.arguments
  if (!isObject(input) || typeof input.description !== 'string' || input.description.trim() === '' || typeof input.prompt !== 'string' || input.prompt.trim() === '' || input.subagent_type !== 'general-purpose')
    throw new Error('The Pi Agent call contains invalid child arguments.')
  const result = nativeToolResult(request, spawnCallId)
  if (!/^Agent (?:started|queued) in background\.$/m.test(result))
    throw new Error('The Pi child receipt contains no successful background launch.')
  const ids = [...result.matchAll(/^Agent ID: ([\da-f]{8}-[\da-f]{4}-[\da-f]{3})$/gm)]
  const files = [...result.matchAll(/^Output file: ([^\r\n]+)$/gm)]
  const childId = ids[0]?.[1]
  if (ids.length !== 1 || !childId || files.length > 1)
    throw new Error('The Pi child receipt contains no unique native child identity.')
  const outputFile = files[0]?.[1]
  return { spawnCallId, description: input.description, childId, ...(outputFile === undefined ? {} : { outputFile }) }
}

/** Match the completion envelope of one actual Pi child, with its call and report. */
export function piChildNoticeRule(launch: PiChildLaunch, options: PiNoticeOptions): MockModelRule {
  for (const field of ['name', 'spawnCallId', 'description', 'report', 'reply'] as const)
    requireNotice(options[field], field)
  if (launch.spawnCallId !== options.spawnCallId || launch.description !== options.description)
    throw new Error('The Pi notification does not identify its actual Agent call.')
  if (!/^[\da-f]{8}-[\da-f]{4}-[\da-f]{3}$/.test(launch.childId))
    throw new Error('The Pi notification requires an actual native child ID.')
  if (launch.outputFile !== undefined)
    requireNotice(launch.outputFile, 'outputFile')
  const usage = '<usage><total_tokens>\\d+</total_tokens><tool_uses>\\d+</tool_uses>'
    + '(?:<context_percent>\\d+</context_percent>)?(?:<compactions>\\d+</compactions>)?'
    + '(?:<estimated_cost_usd>\\d+\\.\\d+</estimated_cost_usd>)?<duration_ms>\\d+</duration_ms></usage>'
  const envelope = `^<task-notification>\\n`
    + `<task-id>${escapeRegExp(launch.childId)}</task-id>\\n`
    + `<tool-use-id>${escapeRegExp(encodeNativeXmlText(options.spawnCallId))}</tool-use-id>\\n${
      launch.outputFile === undefined ? '' : `<output-file>${escapeRegExp(encodeNativeXmlText(launch.outputFile))}</output-file>\\n`
    }<status>Done</status>\\n`
    + `<summary>Agent "${escapeRegExp(encodeNativeXmlText(options.description))}" completed</summary>\\n`
    + `<result>${escapeRegExp(encodeNativeXmlText(options.report))}</result>\\n${usage}\\n</task-notification>${
      launch.outputFile === undefined ? '' : `\\nFull transcript available at: ${escapeRegExp(launch.outputFile)}`
    }$`
  return {
    name: options.name,
    when: { protocol: 'openai-chat-completions', user: envelope, lastMessage: { role: 'user', text: envelope } },
    respond: { text: options.reply },
    ...(options.once === undefined ? {} : { once: options.once }),
  }
}

/** Register the actual child identity before its controlled model answer can complete. */
export async function registerPiChildNoticeRule(modelScript: Pick<ModelScript, 'status' | 'rule'>, options: PiNoticeOptions): Promise<MockModelRule> {
  const launch = await waitForNewestModelRequest(modelScript, request => piChildLaunch(request, options.spawnCallId))
  const rule = piChildNoticeRule(launch, options)
  await modelScript.rule(rule)
  return rule
}

/** Keep a native workflow notice separate from an individual child notice. */
export function piWorkflowNoticeRule(options: PiWorkflowNoticeOptions): MockModelRule {
  for (const field of ['name', 'taskId', 'callId', 'workflowName', 'reply'] as const)
    requireNotice(options[field], field)
  if (!/^wf_[\w-]+$/.test(options.taskId) || options.reports.length === 0)
    throw new Error('The Pi workflow notification requires an actual task ID and reports.')
  for (const report of options.reports)
    requireNotice(report, 'report')
  if (options.scriptPath !== undefined)
    requireNotice(options.scriptPath, 'scriptPath')
  const reports = options.reports.map(report => `(?=[^<]*${escapeRegExp(encodeNativeXmlText(report))})`).join('')
  // Pi's formatter counts raw progress records in the denominator.
  // Keep the completed child count exact and reject a smaller total.
  const smallerTotals = Array.from({ length: options.reports.length }, (_, index) => String(index)).join('|')
  const rawTotal = `(?!(?:${smallerTotals}) agents)[1-9]\\d*`
  const envelope = `^<task-notification>\\n`
    + `<task-id>${escapeRegExp(options.taskId)}</task-id>\\n<tool-use-id>${escapeRegExp(encodeNativeXmlText(options.callId))}</tool-use-id>\\n${
      options.scriptPath === undefined ? '' : `<script>${escapeRegExp(encodeNativeXmlText(options.scriptPath))}</script>\\n`
    }<status>Done</status>\\n`
    + `<summary>Workflow "${escapeRegExp(encodeNativeXmlText(options.workflowName))}" completed — ${options.reports.length}/${rawTotal} agents</summary>\\n`
    + `<result>${reports}[^<]*</result>\\n`
    + `<usage><total_tokens>\\d+</total_tokens><tool_uses>\\d+</tool_uses><duration_ms>\\d+</duration_ms></usage>\\n</task-notification>$`
  return {
    name: options.name,
    when: { protocol: 'openai-chat-completions', user: envelope, lastMessage: { role: 'user', text: envelope } },
    respond: { text: options.reply },
    ...(options.once === undefined ? {} : { once: options.once }),
  }
}
