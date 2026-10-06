import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { BackgroundTaskKind } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { hasNativeToolResult, nativeToolCallArguments, nativeToolResult } from '../helpers/nativeToolResult'
import { encodeNativeXmlText } from '../helpers/nativeXml'
import { waitForNewestModelRequest } from '../helpers/newestModelRequest'
import { requireNonemptyText } from '../helpers/requiredText'
import { retryUntilPass } from '../helpers/retryUntilPass'

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

/** Refuse a notice input that holds no text. */
function requireNotice(value: string, field: string): void {
  requireNonemptyText(value, 'The Letta notification', field)
}

/** Read one native task receipt from the actual result of its Agent call. */
export function lettaChildLaunch(request: MockModelRequestRecord, spawnCallId: string): LettaChildLaunch | null {
  requireNotice(spawnCallId, 'spawnCallId')
  if (request.protocol !== 'openai-chat-completions' || !hasNativeToolResult(request, spawnCallId))
    return null
  const call = nativeToolCallArguments(request, spawnCallId)
  if (call.name !== 'Agent')
    throw new Error('The Letta child receipt contains no unique actual Agent call.')
  const input = call.arguments
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
    requireNotice(options[field], field)
  if (launch.spawnCallId !== options.spawnCallId || launch.description !== options.description)
    throw new Error('The Letta notification does not identify its actual Agent call.')
  requireNotice(launch.outputFile, 'outputFile')
  if (!/^task_\d+$/.test(launch.taskId) || !/^subagent-\d+-\d+$/.test(childId))
    throw new Error('The Letta notification requires actual native task and subagent IDs.')
  for (const field of ['agentId', 'conversationId'] as const) {
    if (launch[field] !== undefined)
      requireNotice(launch[field], field)
  }
  const header = `subagent_type=general-purpose subagent_id=${escapeRegExp(childId)} subagent_status=success${
    launch.agentId === undefined ? '(?: agent_id=[^\\s<>]+)?' : ` agent_id=${escapeRegExp(encodeNativeXmlText(launch.agentId))}`
  }${launch.conversationId === undefined ? '(?: conversation_id=[^\\s<>]+)?' : ` conversation_id=${escapeRegExp(encodeNativeXmlText(launch.conversationId))}`
  }(?: runtime_session_id=[^\\s<>]+)?`
  const usage = '(?:\\n<usage>(?:total_tokens: \\d+(?:\\ntool_uses: \\d+)?(?:\\nduration_ms: \\d+)?|tool_uses: \\d+(?:\\nduration_ms: \\d+)?|duration_ms: \\d+)</usage>)?'
  const envelope = '^<task-notification>\\n'
    + `<task-id>${escapeRegExp(launch.taskId)}</task-id>\\n<status>completed</status>\\n`
    + `<summary>${escapeRegExp(encodeNativeXmlText(`Agent "${options.description}" completed`))}</summary>\\n`
    + `<result>${header}\\n\\n(?=[^<]*${escapeRegExp(encodeNativeXmlText(options.report))})[^<]*</result>${usage}\\n</task-notification>\\n`
    + `Full transcript available at: ${escapeRegExp(launch.outputFile)}$`
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
  const launch = await waitForNewestModelRequest(context.modelScript, request => lettaChildLaunch(request, options.spawnCallId))
  const rows = await retryUntilPass(async () => {
    const found = (await readNativeSidebarSnapshot(context, parent.id)).backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT && task.title === options.description && task.childAgentId !== '')
    expect(found.length, 'the Worker holds the actual child row of the Letta notification').toBeGreaterThan(0)
    return found
  })
  const [row, ...others] = rows
  if (!row || others.length > 0)
    throw new Error('The Letta notification matches more than one actual child row.')
  const childId = row.id
  const rule = lettaChildNoticeRule(launch, childId, options)
  await context.modelScript.rule(rule)
  return rule
}
