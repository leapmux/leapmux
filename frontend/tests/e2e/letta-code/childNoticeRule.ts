import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { BackgroundTaskKind } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { nativeToolResult } from '../helpers/nativeToolResult'

export interface LettaChildLaunch {
  spawnCallId: string
  description: string
  taskId: string
  outputFile: string
  agentId?: string
  conversationId?: string
}

interface LettaNoticeOptions {
  name: string
  spawnCallId: string
  description: string
  report: string
  reply: string
  once?: boolean
}

function literalPattern(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// Letta escapes these characters before it writes summary and result text.
function xmlText(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

function requireText(value: string, field: string): void {
  if (typeof value !== 'string' || value.trim() === '')
    throw new Error(`The Letta notification requires nonempty text for ${field}.`)
}

/** Read one native task receipt from the actual result of its Agent call. */
export function lettaChildLaunch(request: MockModelRequestRecord, spawnCallId: string): LettaChildLaunch | null {
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
    throw new Error('The Letta child receipt contains no unique actual Agent call.')
  let input: unknown
  try {
    input = JSON.parse(call.function.arguments)
  }
  catch (error) {
    throw new Error('The Letta Agent call contains invalid JSON arguments.', { cause: error })
  }
  if (!isObject(input) || typeof input.description !== 'string' || input.description.trim() === '' || typeof input.prompt !== 'string' || input.prompt.trim() === '' || input.subagent_type !== 'general-purpose')
    throw new Error('The Letta Agent call contains invalid child arguments.')
  const result = nativeToolResult(request, spawnCallId)
  const tasks = [...result.matchAll(/^Task running in background with task ID: (task_\d+)$/gm)]
  const files = [...result.matchAll(/^Output file: ([^\r\n]+)$/gm)]
  const agents = [...result.matchAll(/^Agent ID: (\S+)$/gm)]
  const conversations = [...result.matchAll(/^Conversation ID: (\S+)$/gm)]
  const taskId = tasks[0]?.[1]
  const outputFile = files[0]?.[1]
  if (tasks.length !== 1 || !taskId || files.length !== 1 || !outputFile || agents.length > 1 || conversations.length > 1)
    throw new Error('The Letta child receipt contains no unique native task identity.')
  const agentId = agents[0]?.[1]
  const conversationId = conversations[0]?.[1]
  return { spawnCallId, description: input.description, taskId, outputFile, ...(agentId === undefined ? {} : { agentId }), ...(conversationId === undefined ? {} : { conversationId }) }
}

/** Match one actual Letta task and its native subagent report. */
export function lettaChildNoticeRule(launch: LettaChildLaunch, childId: string, options: LettaNoticeOptions): MockModelRule {
  for (const field of ['name', 'spawnCallId', 'description', 'report', 'reply'] as const)
    requireText(options[field], field)
  if (launch.spawnCallId !== options.spawnCallId || launch.description !== options.description)
    throw new Error('The Letta notification does not identify its actual Agent call.')
  requireText(launch.outputFile, 'outputFile')
  if (!/^task_\d+$/.test(launch.taskId) || !/^subagent-\d+-\d+$/.test(childId))
    throw new Error('The Letta notification requires actual native task and subagent IDs.')
  for (const field of ['agentId', 'conversationId'] as const) {
    if (launch[field] !== undefined)
      requireText(launch[field], field)
  }
  const header = `subagent_type=general-purpose subagent_id=${literalPattern(childId)} subagent_status=success${
    launch.agentId === undefined ? '(?: agent_id=[^\\s<>]+)?' : ` agent_id=${literalPattern(xmlText(launch.agentId))}`
  }${launch.conversationId === undefined ? '(?: conversation_id=[^\\s<>]+)?' : ` conversation_id=${literalPattern(xmlText(launch.conversationId))}`
  }(?: runtime_session_id=[^\\s<>]+)?`
  const usage = '(?:\\n<usage>(?:total_tokens: \\d+(?:\\ntool_uses: \\d+)?(?:\\nduration_ms: \\d+)?|tool_uses: \\d+(?:\\nduration_ms: \\d+)?|duration_ms: \\d+)</usage>)?'
  const envelope = '^<task-notification>\\n'
    + `<task-id>${literalPattern(launch.taskId)}</task-id>\\n<status>completed</status>\\n`
    + `<summary>${literalPattern(xmlText(`Agent "${options.description}" completed`))}</summary>\\n`
    + `<result>${header}\\n\\n(?=[^<]*${literalPattern(xmlText(options.report))})[^<]*</result>${usage}\\n</task-notification>\\n`
    + `Full transcript available at: ${literalPattern(launch.outputFile)}$`
  return {
    name: options.name,
    when: { protocol: 'openai-chat-completions', user: envelope, lastMessage: { role: 'user', text: envelope } },
    respond: { text: options.reply },
    ...(options.once === undefined ? {} : { once: options.once }),
  }
}

/** Read both native identities before the controlled child can deliver its report. */
export async function registerLettaChildNoticeRule(context: ManagedNativeScenarioContext, options: LettaNoticeOptions): Promise<MockModelRule> {
  const parent = await currentNativeAgent(context)
  let launch: LettaChildLaunch | null = null
  await expect.poll(async () => {
    for (const request of [...(await context.modelScript.status()).requests].reverse()) {
      const candidate = lettaChildLaunch(request, options.spawnCallId)
      if (candidate) {
        launch = candidate
        return true
      }
    }
    return false
  }).toBe(true)
  let childId = ''
  await expect.poll(async () => {
    const rows = (await readNativeSidebarSnapshot(context, parent.id)).backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT && task.title === options.description && task.childAgentId !== '')
    if (rows.length > 1)
      throw new Error('The Letta notification matches more than one actual child row.')
    childId = rows[0]?.id ?? ''
    return childId !== ''
  }).toBe(true)
  if (!launch)
    throw new Error('The Letta notification has no matching actual Agent receipt.')
  const rule = lettaChildNoticeRule(launch, childId, options)
  await context.modelScript.rule(rule)
  return rule
}
