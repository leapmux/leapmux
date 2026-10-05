import type { Locator, Page } from '@playwright/test'
import type { AgentInfo, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentStatus, ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'
import { googleFunctionDeclarations, googleLastUserText, googlePartsText } from './googleModelContent'
import { jsonStringValues } from './jsonStringValues'

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
}

/** A scenario that can inspect the Worker and manage the current native session. */
export interface ManagedNativeScenarioContext extends NativeScenarioContext {
  leapmuxServer: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>
    & Partial<Pick<ServerInfo, 'agentEnv' | 'adminUserId' | 'mockModelUrl'>>
  workspaceId: string
}

/** Build a native answer while keeping provider decisions at the call site. */
export function nativeTextStep(context: NativeScenarioContext, text: string): MockModelStep {
  return context.textStep?.(text) ?? { text }
}

/** Read one actual Worker agent without using the Hub's optimistic tab list. */
export async function nativeAgentById(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
): Promise<AgentInfo | null> {
  if (!agentId)
    throw new Error('The native agent read requires an agent ID.')
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(
    server.workerId,
    'ListAgents',
    ListAgentsRequestSchema,
    ListAgentsResponseSchema,
    { tabIds: [agentId] },
  )
  return response.agents.find(agent => agent.id === agentId) ?? null
}

/** Locate the selected agent tab of the visible tab bar. The tab identifies an agent. Its state is not a Worker verdict. */
export function selectedAgentTab(page: Page): Locator {
  return page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible').first()
}

/** Resolve the active tab to a successfully started native Worker agent. */
export async function currentNativeAgent(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>): Promise<AgentInfo> {
  const tab = selectedAgentTab(context.page)
  await expect(tab).toBeVisible()
  const agentId = await tab.getAttribute('data-tab-id')
  if (!agentId)
    throw new Error('The active native agent tab has no agent ID.')
  await expect.poll(async () => (await nativeAgentById(context, agentId))?.status).toBe(AgentStatus.ACTIVE)
  const agent = await nativeAgentById(context, agentId)
  if (!agent || agent.status !== AgentStatus.ACTIVE)
    throw new Error('The active native agent did not complete startup.')
  return agent
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
