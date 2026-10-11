import type { Locator, Page } from '@playwright/test'
import type { AgentInfo, AgentProvider, AvailableOptionGroup } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { AgentStatus, ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'
import { googleLastUserText, googlePartsText } from './googleModelContent'
import { jsonStringValues } from './jsonStringValues'
import { isSystemRow, requestRows, requestSystemFields, requestToolDescriptors, rowContent } from './modelRequestBody'
import { nativeToolResult } from './nativeToolResult'
import { retryUntilPass } from './retryUntilPass'
import { AGENT_TAB_SELECTOR } from './tabSelectors'

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

/** A model call and the selected Worker transcript whose browser row the provider resolves. */
export interface NativeToolRowIdQuery {
  callId: string
  agentId: string
}

export type NativeToolRowIdResolver = (query: NativeToolRowIdQuery) => Promise<string>

/** A native browser scenario with a scripted model and provider-owned tool vocabulary. */
export interface NativeScenarioContext {
  page: Page
  modelScript: ModelScript
  provider: AgentProvider
  textStep?: NativeTextStep
  /**
   * The names of model tool calls that deliver the provider's final answer without a transcript tool row, such as Junie's `answer`.
   * A turn that holds only such a call has no tool activity.
   */
  answerToolNames?: readonly string[]
  readToolResult?: NativeToolResultReader
  readModelContext?: NativeModelContextReader
  /**
   * Read the user and assistant turns of a native model request.
   * Supply this reader when the provider's request history differs from the generic shape that `nativeModelConversationTurns` reads.
   */
  readConversationTurns?: (request: MockModelRequestRecord) => NativeModelTurn[]
  /** Resolve browser row identity from actual Worker frames. Model receipts keep their original call IDs. */
  resolveToolRowId?: NativeToolRowIdResolver
}

/** A scenario that can inspect the Worker and manage the current native session. */
export interface ManagedNativeScenarioContext extends NativeScenarioContext {
  leapmuxServer: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>
    & Partial<Pick<ServerInfo, 'agentEnv' | 'adminUserId' | 'mockModelUrl'>>
  workspaceId: string
  /**
   * How an agent of `provider` opens: its prefix and the rule that creates its working directory.
   * Its own `provider` field holds the same value as this context's `provider`.
   * `newNativeWorkingDir` in `./nativeAgentOpen.ts` creates each new native agent's working directory by this rule.
   * A provider that reads configuration from the surrounding git repository opens in a repository of its own.
   */
  providerAgent: ProviderAgent
}

/**
 * The fixtures that the `nativeContext` of a provider directory builds its context from.
 * The provider supplies its `ProviderAgent` through {@link managedNativeContext}.
 * It also supplies the fields of {@link NativeProtocol} that its native protocol needs.
 */
export type NativeContextFixtures = Pick<ManagedNativeScenarioContext, 'page' | 'modelScript' | 'leapmuxServer' | 'workspaceId'>

/**
 * The fields of a scenario context that the native protocol of a provider states:
 *
 * - `textStep`.
 * - `answerToolNames`.
 * - `readToolResult`.
 * - `readModelContext`.
 * - `readConversationTurns`.
 * - `resolveToolRowId`.
 *
 * A provider states only the fields that its protocol needs.
 */
export type NativeProtocol = Omit<NativeScenarioContext, 'page' | 'modelScript' | 'provider'>

/**
 * Build the managed scenario context for the provider directory's `nativeContext`.
 * The context takes its `provider` from `providerAgent`, so the two cannot differ.
 */
export function managedNativeContext(
  fixtures: NativeContextFixtures,
  providerAgent: ProviderAgent,
  protocol: NativeProtocol = {},
): ManagedNativeScenarioContext {
  return { ...fixtures, ...protocol, provider: providerAgent.provider, providerAgent }
}

/** Build a native answer while keeping provider decisions at the call site. */
export function nativeTextStep(context: NativeScenarioContext, text: string): MockModelStep {
  return context.textStep?.(text) ?? { text }
}

/**
 * Where a native turn states its answer after its tool calls:
 *
 * - `next-step`: in the model step after the tool calls. That step reads the results. This is the usual exchange.
 * - `same-step`: in the step that calls the tools. Use this when one response streams both tool calls and the answer.
 *   Cursor's Run exchange holds the whole turn in this way.
 */
export type NativeAnswerStep = 'next-step' | 'same-step'

/**
 * Build the model steps of one turn that calls `toolCalls` and answers with `answer`. Use the stated place of the answer.
 * An answer that holds tool calls of its own, such as an answer tool, keeps them after `toolCalls` in one step.
 */
export function toolTurnSteps(toolCalls: readonly MockModelToolCall[], answer: MockModelStep, answerStep: NativeAnswerStep = 'next-step'): MockModelStep[] {
  if (toolCalls.length === 0)
    throw new Error('A tool turn needs at least one tool call.')
  if (answerStep === 'same-step')
    return [{ ...answer, toolCalls: [...toolCalls, ...answer.toolCalls ?? []] }]
  return [{ toolCalls: [...toolCalls] }, answer]
}

/**
 * Read the actual Worker agents of `agentIds`, without the Hub's optimistic tab list.
 * The Worker lists the agents that it holds, so an ID that it does not hold is absent from the result.
 * A failed read throws its error, for example while the Worker reconnects.
 *
 * Wait on this read with `retryUntilPass` (`./retryUntilPass.ts`), and put the assertion inside the attempt.
 * `expect.poll` is not a correct wait here. Playwright calls the poll function outside the `try` that retries a failed matcher.
 * Thus the first thrown read ends the poll at once.
 * `retryUntilPass` retries a thrown read and a failed assertion the same way. Its final failure states the last error.
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
  return page.locator(`${AGENT_TAB_SELECTOR}[aria-selected="true"]:visible`).first()
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

/**
 * The identity of one native session:
 *
 * - The Worker agent.
 * - Its native session.
 * - Its working directory.
 */
export type NativeSessionIdentity = Pick<AgentInfo, 'id' | 'agentSessionId' | 'workingDir'>

/**
 * Require that `after` keeps the identity of `before`:
 *
 * - The same Worker agent.
 * - The same native session.
 * - The same working directory.
 *
 * An operation that reads a provider's native catalog or metadata must not start another agent or session.
 * `operation` identifies the operation in the failure, such as `The native Cline catalog read`.
 */
export function expectSameNativeSession(before: NativeSessionIdentity, after: NativeSessionIdentity, operation: string): void {
  const identity = (agent: NativeSessionIdentity) => ({ id: agent.id, agentSessionId: agent.agentSessionId, workingDir: agent.workingDir })
  expect(identity(after), `${operation} keeps the native agent, its session, and its working directory`).toEqual(identity(before))
}

/**
 * Read submitted or server-held native context from a recorded model request.
 * Read each string value of the body and the server-held context, in document order, one per line.
 *
 * The text keeps each string literal. JSON encoding escapes quotes and backslashes. It also escapes line breaks.
 * A literal marker with those characters cannot match encoded text. A negative check against encoded text cannot prove the model lacked that marker.
 */
export function nativeModelContextText(
  request: MockModelRequestRecord & { serverContext?: unknown },
): string {
  return jsonStringValues([request.body, request.serverContext]).join('\n')
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

/**
 * One tool that a native model request offers:
 *
 * - Its name.
 * - Its description.
 * - Its argument schema.
 */
export interface NativeToolDescriptor {
  name: string
  description: string
  inputSchema: Record<string, unknown>
}

/**
 * Read each tool descriptor of a generic native model API out of its envelope.
 * Reject a missing or empty catalog. The descriptors stay raw, so a provider reader applies its own checks to each field.
 */
export function nativeModelToolDescriptors(request: Pick<MockModelRequestRecord, 'protocol' | 'body'>): Record<string, unknown>[] {
  const tools = requestToolDescriptors(request.protocol, request.body)
  if (!tools || tools.length === 0)
    throw new Error('The native model request contains no nonempty tool catalog.')
  return tools
}

/** Read the tool catalog of a generic native model API without accepting a missing catalog. */
export function nativeModelToolNames(request: Pick<MockModelRequestRecord, 'protocol' | 'body'>): string[] {
  return nativeModelToolDescriptors(request).map((tool) => {
    if (typeof tool.name !== 'string' || tool.name === '')
      throw new Error('The native model tool catalog contains an entry without a name.')
    return tool.name
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
  const rows = requestRows(request.protocol, body)
  if (request.protocol === 'google-generative-language') {
    const text = googleLastUserText(rows)
    if (text === '')
      throw new Error('The native model request contains no last user text.')
    return text
  }
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

/**
 * Read generic model instructions from the protocol fields that ./modelRequestBody.ts states:
 *
 * - The system fields outside the rows.
 * - The text of each system row and user row, in conversation order.
 *
 * Exclude tool schemas and tool results. Exclude assistant answers also.
 * A Responses `input` string is user text. A Google part has no block type, so each text part counts.
 */
export function nativeModelInstructionText(request: MockModelRequestRecord): string {
  const { protocol, body } = request
  if (protocol === 'aws-event-stream')
    throw new Error('The native service must supply its own instruction reader.')
  if (!isObject(body))
    throw new Error('The native instruction request body must be an object.')
  const parts = requestSystemFields(protocol, body).flatMap(content => nativeTextBlocks(content))
  const rows = requestRows(protocol, body)
  if (protocol === 'openai-responses' && typeof rows === 'string') {
    parts.push(rows)
  }
  else if (Array.isArray(rows)) {
    for (const row of rows) {
      if (isObject(row) && (isSystemRow(protocol, row) || row.role === 'user'))
        parts.push(...nativeTextBlocks(rowContent(protocol, row)))
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
   * The text blocks of the turn, joined with newlines.
   * An assistant turn also holds string values from its tool-call arguments because a provider can deliver its final answer through a tool.
   * A tool result is not user text, and reasoning is not assistant text.
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
  if (!isObject(request.body))
    throw new Error('The native conversation request body must be an object.')
  const rows = requestRows(request.protocol, request.body)
  if (request.protocol === 'google-generative-language')
    return googleConversationTurns(rows)
  if (request.protocol === 'openai-responses')
    return responsesConversationTurns(rows)
  return messageConversationTurns(rows)
}
