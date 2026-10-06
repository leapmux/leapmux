import type { Locator, Page } from '@playwright/test'
import type { AgentInfo, AgentProvider, AvailableOptionGroup } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentStatus, ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'
import { googleFunctionDeclarations, googleLastUserText, googlePartsText } from './googleModelContent'
import { jsonStringValues } from './jsonStringValues'
import { nativeToolResult } from './nativeToolResult'
import { retryUntilPass } from './retryUntilPass'

/** The provider supplies any native final-answer tool through this callback. */
export type NativeTextStep = (text: string) => MockModelStep

/** An actual tool result. Missing native status fields stay absent. */
export interface NativeToolOutcome {
  text: string
  failed?: boolean
  exitCode?: number
}

export type NativeToolResultReader = (request: MockModelRequestRecord, callId: string) => NativeToolOutcome | Promise<NativeToolOutcome>

export type NativeModelContextReader = (request: MockModelRequestRecord) => string

/** A native browser scenario with a scripted model and provider-owned tool vocabulary. */
export interface NativeScenarioContext {
  page: Page
  modelScript: ModelScript
  provider: AgentProvider
  textStep?: NativeTextStep
  /**
   * The names of the model tool calls that deliver the provider's final answer
   * and that its transcript shows as no tool row (Junie's `answer`). A turn
   * that holds only such a call has no tool activity.
   */
  answerToolNames?: readonly string[]
  readToolResult?: NativeToolResultReader
  readModelContext?: NativeModelContextReader
  /**
   * Read the user and assistant turns of a native model request, for a provider whose request does not state its
   * history in the generic shape that `nativeModelConversationTurns` reads.
   */
  readConversationTurns?: (request: MockModelRequestRecord) => NativeModelTurn[]
}

/** A scenario that can inspect the Worker and manage the current native session. */
export interface ManagedNativeScenarioContext extends NativeScenarioContext {
  leapmuxServer: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>
    & Partial<Pick<ServerInfo, 'agentEnv' | 'adminUserId' | 'mockModelUrl'>>
  workspaceId: string
}

/**
 * The fixtures that the `nativeContext` of a provider directory builds its context from.
 * The provider sets every other field: `provider`, and each of `textStep`, `answerToolNames`, `readToolResult`, and
 * `readModelContext` that its native protocol needs.
 */
export type NativeContextFixtures = Pick<ManagedNativeScenarioContext, 'page' | 'modelScript' | 'leapmuxServer' | 'workspaceId'>

/** Build a native answer while keeping provider decisions at the call site. */
export function nativeTextStep(context: NativeScenarioContext, text: string): MockModelStep {
  return context.textStep?.(text) ?? { text }
}

/**
 * Read the actual Worker agents of `agentIds`, without the Hub's optimistic tab list.
 * The Worker lists the agents that it holds, so an ID that it does not hold is absent from the result.
 * A failed read throws its error, for example while the Worker reconnects.
 *
 * Wait on this read with `retryUntilPass` (`./retryUntilPass.ts`), and put the assertion inside the attempt.
 * `expect.poll` is not a correct wait here: Playwright calls the poll function outside the `try` that retries a failed
 * matcher, so the first thrown read ends the poll at once. `retryUntilPass` retries a thrown read and a failed
 * assertion the same way, and its final failure states the last error.
 */
export async function nativeAgentsByIds(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentIds: readonly string[],
): Promise<AgentInfo[]> {
  if (agentIds.length === 0 || agentIds.some(agentId => !agentId))
    throw new Error('The native agent read requires one or more agent IDs, each nonempty.')
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(
    server.workerId,
    'ListAgents',
    ListAgentsRequestSchema,
    ListAgentsResponseSchema,
    { tabIds: [...agentIds] },
  )
  return response.agents
}

/** Read one actual Worker agent without using the Hub's optimistic tab list. Return null when the Worker holds none. */
export async function nativeAgentById(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
): Promise<AgentInfo | null> {
  if (!agentId)
    throw new Error('The native agent read requires an agent ID.')
  const agents = await nativeAgentsByIds(context, [agentId])
  return agents.find(agent => agent.id === agentId) ?? null
}

/** Locate the selected agent tab of the visible tab bar. The tab identifies an agent. Its state is not a Worker verdict. */
export function selectedAgentTab(page: Page): Locator {
  return page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible').first()
}

/**
 * Read the agent ID of the selected agent tab.
 * The selected tab is the agent on screen. The first tab is the selected one only while the workspace has one agent.
 */
export async function selectedAgentTabId(page: Page): Promise<string> {
  const tab = selectedAgentTab(page)
  await expect(tab).toBeVisible()
  const agentId = await tab.getAttribute('data-tab-id')
  if (!agentId)
    throw new Error('The selected agent tab has no agent ID in its data-tab-id attribute.')
  return agentId
}

/** Return the option group `groupId` of the agent's catalog, or undefined when the catalog has none. */
export function nativeOptionGroup(agent: Pick<AgentInfo, 'optionGroups'>, groupId: string): AvailableOptionGroup | undefined {
  return agent.optionGroups.find(group => group.id === groupId)
}

/** Return the current value of the option group `groupId`, or undefined when the catalog has no such group. */
export function nativeOptionValue(agent: Pick<AgentInfo, 'optionGroups'>, groupId: string): string | undefined {
  return nativeOptionGroup(agent, groupId)?.currentValue
}

/**
 * Require `value` as the current value of the option group `groupId` of the active native agent.
 * A catalog without the group fails with a message that states the group, not with "expected undefined".
 */
export async function expectNativeOptionValue(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  groupId: string,
  value: string,
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const group = nativeOptionGroup(agent, groupId)
  if (!group)
    throw new Error(`The native catalog has no option group ${groupId}. It has ${agent.optionGroups.map(candidate => candidate.id).join(', ') || 'no group'}.`)
  expect(group.currentValue, `the current value of the native option group ${groupId}`).toBe(value)
}

/** Resolve the active tab to a successfully started native Worker agent. */
export async function currentNativeAgent(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>): Promise<AgentInfo> {
  const agentId = await selectedAgentTabId(context.page)
  return retryUntilPass(async () => {
    const agent = await nativeAgentById(context, agentId)
    if (!agent)
      throw new Error(`The Worker holds no native agent ${agentId}.`)
    expect(agent.status, `the Worker reports the selected native agent ${agentId} as active`).toBe(AgentStatus.ACTIVE)
    return agent
  })
}

/** Read submitted or server-held native context from a recorded model request. */
export function nativeModelContextText(
  request: MockModelRequestRecord & { serverContext?: unknown },
): string {
  return JSON.stringify({ body: request.body, serverContext: request.serverContext })
}

/** Read context through the scenario's provider reader or the generic service reader. */
export function nativeScenarioModelContextText(
  context: Pick<NativeScenarioContext, 'readModelContext'>,
  request: MockModelRequestRecord,
): string {
  return context.readModelContext?.(request) ?? nativeModelContextText(request)
}

/**
 * Read the result of one tool call through the scenario's provider reader, or through the generic reader.
 * The generic reader states only the result text. A provider reader can also state the failure and the exit code.
 */
export async function nativeToolOutcome(
  context: { readonly readToolResult?: NativeToolResultReader | undefined },
  request: MockModelRequestRecord,
  callId: string,
): Promise<NativeToolOutcome> {
  return context.readToolResult ? context.readToolResult(request, callId) : { text: nativeToolResult(request, callId) }
}

/** Read the last queued native request body. Keep fallback and rule requests separate. */
export function nativeLastStepBody(requests: readonly MockModelRequestRecord[]): Record<string, unknown> {
  const last = requests.filter(request => request.stepIndex !== undefined).at(-1)
  if (!last || !isObject(last.body))
    throw new Error('The script contains no valid queued model request body.')
  return { ...last.body }
}

/** Join native bodies from the inclusive queued-step index. Keep unqueued requests separate. */
export function nativeModelBodiesAfter(status: { requests: readonly Pick<MockModelRequestRecord, 'stepIndex' | 'body'>[] }, from: number): string {
  return status.requests.filter(request => (request.stepIndex ?? -1) >= from).map(request => JSON.stringify(request.body)).join('\n')
}

/** Read the tool catalog of a generic native model API without accepting a missing catalog. */
export function nativeModelToolNames(request: MockModelRequestRecord): string[] {
  const body = request.body
  if (typeof body !== 'object' || body === null || !('tools' in body) || !Array.isArray(body.tools) || body.tools.length === 0)
    throw new Error('The native model request contains no nonempty tool catalog.')
  const tools = request.protocol === 'google-generative-language' ? googleFunctionDeclarations(body.tools) : body.tools
  if (tools.length === 0)
    throw new Error('The native model request contains no nonempty tool catalog.')
  return tools.map((tool: unknown) => {
    if (typeof tool !== 'object' || tool === null || Array.isArray(tool))
      throw new Error('The native model tool catalog contains an invalid entry.')
    const direct = 'name' in tool ? tool.name : undefined
    const nested = 'function' in tool && typeof tool.function === 'object' && tool.function !== null && 'name' in tool.function
      ? tool.function.name
      : undefined
    const custom = 'type' in tool && tool.type === 'custom' && 'custom' in tool && isObject(tool.custom)
      ? tool.custom.name
      : undefined
    const name = direct ?? nested ?? custom
    if (typeof name !== 'string' || name === '')
      throw new Error('The native model tool catalog contains an entry without a name.')
    return name
  })
}

/** The text block types of a user turn. A tool result is not user text. */
const USER_TEXT_BLOCK_TYPES: ReadonlySet<unknown> = new Set([undefined, 'text', 'input_text'])

/** The text block types of an assistant turn. Reasoning is not answer text. */
const ASSISTANT_TEXT_BLOCK_TYPES: ReadonlySet<unknown> = new Set([undefined, 'text', 'output_text'])

function nativeTextBlocks(value: unknown, types: ReadonlySet<unknown> = USER_TEXT_BLOCK_TYPES): string[] {
  if (typeof value === 'string')
    return [value]
  if (!Array.isArray(value))
    return []
  return value.flatMap((block: unknown) => {
    if (typeof block !== 'object' || block === null || !('text' in block) || typeof block.text !== 'string')
      return []
    const type = 'type' in block ? block.type : undefined
    return types.has(type) ? [block.text] : []
  })
}

/** Read only the last real user text. Earlier prompts and tool schemas cannot prove a selected mode. */
export function nativeModelLastUserText(request: MockModelRequestRecord): string {
  if (request.protocol === 'aws-event-stream')
    throw new Error('The native service must supply its own reader for the last user message.')
  const body = request.body
  if (!isObject(body))
    throw new Error('The native user request body must be an object.')
  if (request.protocol === 'google-generative-language') {
    const text = googleLastUserText(body.contents)
    if (text === '')
      throw new Error('The native model request contains no last user text.')
    return text
  }
  const rows = request.protocol === 'openai-responses' ? body.input : body.messages
  let parts: string[]
  if (request.protocol === 'openai-responses' && typeof rows === 'string') {
    parts = [rows]
  }
  else {
    if (!Array.isArray(rows))
      throw new Error('The native model request contains no user message array.')
    const last = rows.findLast((row: unknown) => isObject(row) && row.role === 'user')
    if (!isObject(last))
      throw new Error('The native model request contains no user message.')
    parts = nativeTextBlocks(last.content)
  }
  const text = parts.join('\n')
  if (text.length === 0)
    throw new Error('The native model request contains no last user text.')
  return text
}

/** Read generic model instructions without tool schemas, results, or assistant answers. */
export function nativeModelInstructionText(request: MockModelRequestRecord): string {
  if (request.protocol === 'aws-event-stream')
    throw new Error('The native service must supply its own instruction reader.')
  const body = request.body
  if (!isObject(body))
    throw new Error('The native instruction request body must be an object.')
  const parts: string[] = []
  if (request.protocol === 'google-generative-language') {
    if (isObject(body.systemInstruction))
      parts.push(googlePartsText(body.systemInstruction.parts))
    if (Array.isArray(body.contents)) {
      for (const row of body.contents) {
        if (isObject(row) && row.role === 'user')
          parts.push(googlePartsText(row.parts))
      }
    }
  }
  if ('system' in body)
    parts.push(...nativeTextBlocks(body.system))
  if ('instructions' in body)
    parts.push(...nativeTextBlocks(body.instructions))
  for (const key of ['messages', 'input']) {
    if (!(key in body))
      continue
    const rows = body[key]
    if (typeof rows === 'string' && key === 'input') {
      parts.push(rows)
      continue
    }
    if (!Array.isArray(rows))
      continue
    for (const row of rows) {
      if (typeof row !== 'object' || row === null || !('role' in row) || !('content' in row))
        continue
      if (row.role === 'system' || row.role === 'developer' || row.role === 'user')
        parts.push(...nativeTextBlocks(row.content))
    }
  }
  const text = parts.filter(part => part !== '').join('\n')
  if (text === '')
    throw new Error('The native model request contains no instruction or user text.')
  return text
}

/** One user or assistant turn of a native model request. */
export interface NativeModelTurn {
  readonly role: 'user' | 'assistant'
  /**
   * The text blocks of the turn, joined with newlines. An assistant turn also holds the string values of its
   * tool-call arguments, because a provider can deliver its final answer through a tool. A tool result is not
   * user text, and reasoning is not assistant text.
   */
  readonly text: string
}

/** Read the string values of tool-call arguments. A JSON string holds the arguments as encoded JSON. */
export function nativeToolArgumentText(value: unknown): string {
  if (typeof value !== 'string')
    return jsonStringValues(value).join('\n')
  let decoded: unknown
  try {
    decoded = JSON.parse(value)
  }
  catch {
    // Arguments that are not JSON stay the literal text that the model wrote.
    return value
  }
  return typeof decoded === 'object' && decoded !== null ? jsonStringValues(decoded).join('\n') : value
}

function turnText(parts: readonly string[]): string {
  return parts.filter(part => part !== '').join('\n')
}

function googleConversationTurns(contents: unknown): NativeModelTurn[] {
  if (!Array.isArray(contents))
    throw new Error('The native Google model request contains no contents array.')
  return contents.flatMap((row: unknown): NativeModelTurn[] => {
    if (!isObject(row))
      return []
    if (row.role === 'user')
      return [{ role: 'user', text: googlePartsText(row.parts) }]
    if (row.role !== 'model')
      return []
    // A Google thought part is reasoning, not answer text.
    const parts = Array.isArray(row.parts) ? row.parts.filter(isObject).filter(part => part.thought !== true) : []
    const calls = parts.flatMap(part => isObject(part.functionCall) ? [nativeToolArgumentText(part.functionCall.args)] : [])
    return [{ role: 'assistant', text: turnText([googlePartsText(parts), ...calls]) }]
  })
}

function responsesConversationTurns(input: unknown): NativeModelTurn[] {
  if (typeof input === 'string')
    return [{ role: 'user', text: input }]
  if (!Array.isArray(input))
    throw new Error('The native Responses model request contains no input array.')
  return input.flatMap((item: unknown): NativeModelTurn[] => {
    if (!isObject(item))
      return []
    if (item.type === 'function_call')
      return [{ role: 'assistant', text: nativeToolArgumentText(item.arguments) }]
    if (item.type === 'custom_tool_call')
      return [{ role: 'assistant', text: nativeToolArgumentText(item.input) }]
    if (item.type !== undefined && item.type !== 'message')
      return []
    if (item.role === 'user')
      return [{ role: 'user', text: turnText(nativeTextBlocks(item.content)) }]
    if (item.role === 'assistant')
      return [{ role: 'assistant', text: turnText(nativeTextBlocks(item.content, ASSISTANT_TEXT_BLOCK_TYPES)) }]
    return []
  })
}

/** Read Chat Completions and Anthropic Messages rows. Each API keeps its tool calls in a different field. */
function messageConversationTurns(messages: unknown): NativeModelTurn[] {
  if (!Array.isArray(messages))
    throw new Error('The native model request contains no message array.')
  return messages.flatMap((message: unknown): NativeModelTurn[] => {
    if (!isObject(message))
      return []
    if (message.role === 'user')
      return [{ role: 'user', text: turnText(nativeTextBlocks(message.content)) }]
    if (message.role !== 'assistant')
      return []
    const chatCalls = Array.isArray(message.tool_calls)
      ? message.tool_calls.filter(isObject).flatMap(call => isObject(call.function) ? [nativeToolArgumentText(call.function.arguments)] : [])
      : []
    const anthropicCalls = Array.isArray(message.content)
      ? message.content.filter(isObject).flatMap(block => block.type === 'tool_use' ? [nativeToolArgumentText(block.input)] : [])
      : []
    return [{ role: 'assistant', text: turnText([...nativeTextBlocks(message.content, ASSISTANT_TEXT_BLOCK_TYPES), ...chatCalls, ...anthropicCalls]) }]
  })
}

/** Read the user and assistant turns of a generic model API request. System and tool-result rows are not turns. */
export function nativeModelConversationTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  if (request.protocol === 'aws-event-stream')
    throw new Error('The native service must supply its own conversation turn reader.')
  const body = request.body
  if (!isObject(body))
    throw new Error('The native conversation request body must be an object.')
  if (request.protocol === 'google-generative-language')
    return googleConversationTurns(body.contents)
  if (request.protocol === 'openai-responses')
    return responsesConversationTurns(body.input)
  return messageConversationTurns(body.messages)
}
