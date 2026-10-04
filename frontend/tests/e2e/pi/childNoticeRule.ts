import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResult } from '../helpers/nativeToolResult'

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

function literalPattern(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// Pi escapes these three characters in its XML text. Quotes stay unchanged.
function xmlText(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

function requireText(value: string, field: string): void {
  if (typeof value !== 'string' || value.trim() === '')
    throw new Error(`The Pi notification requires nonempty text for ${field}.`)
}

/** Read only the actual result of one Agent call from the isolated OpenAI-compatible API. */
export function piChildLaunch(request: MockModelRequestRecord, spawnCallId: string): PiChildLaunch | null {
  requireText(spawnCallId, 'spawnCallId')
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    return null
  if (!request.body.messages.some((message: unknown) => isObject(message) && message.role === 'tool' && message.tool_call_id === spawnCallId))
    return null
  const calls = request.body.messages
    .filter((message: unknown) => isObject(message) && message.role === 'assistant')
    .flatMap(message => isObject(message) && Array.isArray(message.tool_calls) ? message.tool_calls : [])
    .filter((call: unknown) => isObject(call) && call.id === spawnCallId)
  const call: unknown = calls[0]
  if (calls.length !== 1 || !isObject(call) || !isObject(call.function) || call.function.name !== 'Agent' || typeof call.function.arguments !== 'string')
    throw new Error('The Pi child receipt contains no unique actual Agent call.')
  let input: unknown
  try {
    input = JSON.parse(call.function.arguments)
  }
  catch (error) {
    throw new Error('The Pi Agent call contains invalid JSON arguments.', { cause: error })
  }
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
    requireText(options[field], field)
  if (launch.spawnCallId !== options.spawnCallId || launch.description !== options.description)
    throw new Error('The Pi notification does not identify its actual Agent call.')
  if (!/^[\da-f]{8}-[\da-f]{4}-[\da-f]{3}$/.test(launch.childId))
    throw new Error('The Pi notification requires an actual native child ID.')
  if (launch.outputFile !== undefined)
    requireText(launch.outputFile, 'outputFile')
  const usage = '<usage><total_tokens>\\d+</total_tokens><tool_uses>\\d+</tool_uses>'
    + '(?:<context_percent>\\d+</context_percent>)?(?:<compactions>\\d+</compactions>)?'
    + '(?:<estimated_cost_usd>\\d+\\.\\d+</estimated_cost_usd>)?<duration_ms>\\d+</duration_ms></usage>'
  const envelope = `^<task-notification>\\n`
    + `<task-id>${literalPattern(launch.childId)}</task-id>\\n`
    + `<tool-use-id>${literalPattern(xmlText(options.spawnCallId))}</tool-use-id>\\n${
      launch.outputFile === undefined ? '' : `<output-file>${literalPattern(xmlText(launch.outputFile))}</output-file>\\n`
    }<status>Done</status>\\n`
    + `<summary>Agent "${literalPattern(xmlText(options.description))}" completed</summary>\\n`
    + `<result>${literalPattern(xmlText(options.report))}</result>\\n${usage}\\n</task-notification>${
      launch.outputFile === undefined ? '' : `\\nFull transcript available at: ${literalPattern(launch.outputFile)}`
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
  let launch: PiChildLaunch | null = null
  await expect.poll(async () => {
    for (const request of [...(await modelScript.status()).requests].reverse()) {
      const candidate = piChildLaunch(request, options.spawnCallId)
      if (candidate) {
        launch = candidate
        return true
      }
    }
    return false
  }).toBe(true)
  if (!launch)
    throw new Error('The Pi notification has no matching actual Agent receipt.')
  const rule = piChildNoticeRule(launch, options)
  await modelScript.rule(rule)
  return rule
}

/** Keep a native workflow notice separate from an individual child notice. */
export function piWorkflowNoticeRule(options: PiWorkflowNoticeOptions): MockModelRule {
  for (const field of ['name', 'taskId', 'callId', 'workflowName', 'reply'] as const)
    requireText(options[field], field)
  if (!/^wf_[\w-]+$/.test(options.taskId) || options.reports.length === 0)
    throw new Error('The Pi workflow notification requires an actual task ID and reports.')
  for (const report of options.reports)
    requireText(report, 'report')
  if (options.scriptPath !== undefined)
    requireText(options.scriptPath, 'scriptPath')
  const reports = options.reports.map(report => `(?=[^<]*${literalPattern(xmlText(report))})`).join('')
  // Pi's formatter counts raw progress records in the denominator.
  // Keep the completed child count exact and reject a smaller total.
  const smallerTotals = Array.from({ length: options.reports.length }, (_, index) => String(index)).join('|')
  const rawTotal = `(?!(?:${smallerTotals}) agents)[1-9]\\d*`
  const envelope = `^<task-notification>\\n`
    + `<task-id>${literalPattern(options.taskId)}</task-id>\\n<tool-use-id>${literalPattern(xmlText(options.callId))}</tool-use-id>\\n${
      options.scriptPath === undefined ? '' : `<script>${literalPattern(xmlText(options.scriptPath))}</script>\\n`
    }<status>Done</status>\\n`
    + `<summary>Workflow "${literalPattern(xmlText(options.workflowName))}" completed — ${options.reports.length}/${rawTotal} agents</summary>\\n`
    + `<result>${reports}[^<]*</result>\\n`
    + `<usage><total_tokens>\\d+</total_tokens><tool_uses>\\d+</tool_uses><duration_ms>\\d+</duration_ms></usage>\\n</task-notification>$`
  return {
    name: options.name,
    when: { protocol: 'openai-chat-completions', user: envelope, lastMessage: { role: 'user', text: envelope } },
    respond: { text: options.reply },
    ...(options.once === undefined ? {} : { once: options.once }),
  }
}
