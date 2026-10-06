/**
 * Serve Cursor's native remote endpoints and Run stream.
 *
 * cursor-agent calls its own service. This module decodes its requests and selects answers through the shared script.
 * See ./cursorWire for the native wire format.
 * Native probes confirmed the following requirements.
 *
 * Startup calls use default protobuf fields unless they request model data.
 * A zero-byte protobuf response is valid and keeps the CLI on this endpoint.
 * A real GetServerConfig response selects the CLI's built-in endpoint instead.
 *
 * Three model calls require native catalog data:
 * - AvailableModels supplies the model catalog.
 * - GetDefaultModelForCli selects the initial model.
 * - GetUsableModels supplies the usable models.
 *
 * The CLI replaces an invalid catalog response with an empty list.
 * session/new then reports availableModels: [], and LeapMux cannot start a model turn.
 * Missing default or usable model data causes "No model found. Please check your model settings."
 *
 * Run uses HTTP/2. Startup uses HTTP/1.1 on the same port.
 * createDualVersionListener serves both protocols.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { CursorContextRule, CursorExecutionCall, CursorGenerateImageCall, CursorInteractionCall, CursorInteractionReply, CursorMcpCall, CursorModel, CursorTaskCall, CursorTodoCall, CursorTodoStatus } from './cursorWire'
import type { MockModelScriptHost, ModelRequestContext, SelectedModelAnswer } from './mockModelRequest'
import type { MockModelDeliveredError, MockModelError, MockModelServerContext, MockModelToolCall, MockModelUsage } from './mockModelScript'
import type { ModelStream } from './modelStream'
import { Buffer } from 'node:buffer'
import { createHash, randomUUID } from 'node:crypto'
import { gunzipSync } from 'node:zlib'
import { isObject } from '../../../src/lib/jsonPick'
import { cursorErrorResponse } from './cursorErrors'
import { CursorExecution, cursorExecutionCallFrom } from './cursorExecution'
import { cursorRunRequestWitness } from './cursorRequestWire'
import { CursorSubagentExecution } from './cursorSubagentExecution'
import {
  connectEndOfStream,
  connectFrame,
  cursorAttachmentPayloads,
  cursorAvailableModels,
  cursorConversationIdOf,
  cursorDefaultModel,
  cursorGenerateImageCompleted,
  cursorGenerateImageStarted,
  cursorInteractionQuery,
  cursorInteractionResponseOf,
  cursorMcpExec,
  cursorMcpResponseOf,
  cursorPromptOf,
  cursorRequestContextExec,
  cursorRequestContextResponseOf,
  cursorSetBlob,
  cursorTaskCompleted,
  cursorTaskProgress,
  cursorTaskStarted,
  cursorTextDelta,
  cursorThinkingDelta,
  cursorTodoCompleted,
  cursorTodoStarted,
  cursorTurnEnded,
  cursorUsableModels,
  takeConnectFrames,
} from './cursorWire'
import { mockCredentialReceipt } from './mockCredentials'
import { MAX_MOCK_REQUEST_BYTES, writeResponseHeaders } from './mockHttp'
import { AMBIENT_SCENARIO_ID, selectScenarioID } from './mockModelScript'
import { createModelStream } from './modelStream'

/** The bidirectional stream that carries one whole Cursor turn. */
export const CURSOR_RUN_PATH = '/agent.v1.AgentService/Run'

/** Cursor's subagent tool, whose calls this surface turns into Run-stream updates. */
export const CURSOR_TASK_TOOL = 'task'

/** Cursor's native image-generation tool in a Run-stream tool call. */
export const CURSOR_GENERATE_IMAGE_TOOL = 'generateImage'

/** Cursor's native question query in a Run stream. */
export const CURSOR_QUESTION_TOOL = 'askQuestion'

/** Cursor's native plan query in a Run stream. */
export const CURSOR_CREATE_PLAN_TOOL = 'createPlan'

/** Cursor's web-fetch approval query in a Run stream. */
export const CURSOR_WEB_FETCH_TOOL = 'webFetch'

/** Cursor's local MCP execution request in a Run stream. */
export const CURSOR_MCP_TOOL = 'cursorMcp'

/**
 * Cursor's request context query in a Run stream.
 *
 * The backend asks the CLI for the rules that it loaded from the project, and the
 * CLI states them in its answer. The mock records them in the Run request witness.
 */
export const CURSOR_REQUEST_CONTEXT_TOOL = 'cursorRequestContext'

/** The three startup calls that need a real answer; see the note at the top. */
const CURSOR_AVAILABLE_MODELS_PATH = '/aiserver.v1.AiService/AvailableModels'
const CURSOR_USABLE_MODELS_PATH = '/aiserver.v1.AiService/GetUsableModels'
const CURSOR_DEFAULT_MODEL_PATH = '/aiserver.v1.AiService/GetDefaultModelForCli'

/**
 * The model catalogue the mock reports.
 *
 * It is small, but it keeps the SHAPE of the real one, because LeapMux parses
 * that shape. Cursor states a variant's whole metadata inside the bracketed id
 * and nowhere else, and it spells the effort level three ways across its own
 * catalogue. Two of those spellings appear here, so the picker this produces
 * exercises the same parsing a live account does.
 *
 * `default` answers to `auto`, which is the alias LeapMux normalizes to and the
 * model it starts on.
 */
export const CURSOR_MOCK_MODELS: readonly CursorModel[] = [
  {
    name: 'default',
    displayName: 'Auto',
    aliases: ['auto'],
    variants: [{ id: 'default[]', displayName: 'Auto', isDefault: true, parameters: [] }],
  },
  {
    name: 'mock-sonnet',
    displayName: 'Mock Sonnet',
    variants: [
      { id: 'mock-sonnet[context=200k,effort=low]', displayName: 'Mock Sonnet Low', parameters: [{ id: 'context', value: '200k' }, { id: 'effort', value: 'low' }] },
      { id: 'mock-sonnet[context=200k,effort=high]', displayName: 'Mock Sonnet High', parameters: [{ id: 'context', value: '200k' }, { id: 'effort', value: 'high' }] },
    ],
  },
  {
    name: 'mock-grok',
    displayName: 'Mock Grok',
    variants: [
      { id: 'mock-grok[context=256k,reasoning_effort=low]', displayName: 'Mock Grok Low', parameters: [{ id: 'context', value: '256k' }, { id: 'reasoning_effort', value: 'low' }] },
      { id: 'mock-grok[context=256k,reasoning_effort=xhigh]', displayName: 'Mock Grok Extra High', parameters: [{ id: 'context', value: '256k' }, { id: 'reasoning_effort', value: 'xhigh' }] },
    ],
  },
]

/** Cursor's own backend services, which take an all-defaults answer apart from the catalogue. */
const CURSOR_SERVICE_PREFIX = '/aiserver.v1.'

/** The OpenTelemetry endpoint the CLI exports traces to, which needs no answer of substance. */
const CURSOR_TRACE_PATH = '/v1/traces'

/** Whether this request belongs to Cursor's backend rather than to a model API. */
export function isCursorPath(pathname: string): boolean {
  return pathname.startsWith(CURSOR_SERVICE_PREFIX) || pathname === CURSOR_RUN_PATH || pathname === CURSOR_TRACE_PATH
}

interface CursorConversationState {
  scenarioID: string
  messages: MockModelServerContext['messages']
}

export interface CursorSurface {
  handleHttp: (request: IncomingMessage, response: ServerResponse, url: URL) => Promise<boolean>
  clearScenario: (id: string) => void
  close: () => void
}

/** Own Cursor's native Run route and its retained remote conversation context. */
export function createCursorSurface(host: MockModelScriptHost): CursorSurface {
  const conversations = new Map<string, CursorConversationState>()
  return {
    clearScenario: (id) => {
      for (const [conversationID, state] of conversations) {
        if (state.scenarioID === id)
          conversations.delete(conversationID)
      }
    },
    close: () => conversations.clear(),
    handleHttp: async (request, response, url) => {
      if (!isCursorPath(url.pathname))
        return false
      if (url.pathname !== CURSOR_RUN_PATH) {
        answerCursorStartup(request, response, url.pathname)
        return true
      }
      let completedTurn: ((text: string) => void) | undefined
      let activeAnswer: Extract<SelectedModelAnswer, { kind: 'step' }> | undefined
      let nativeError: MockModelDeliveredError | undefined
      await serveCursorRun(request, response, {
        answer: async (prompt, requestFrame) => {
          const conversationID = cursorConversationIdOf(requestFrame)
          const currentScenarioID = selectScenarioID(prompt)
          let scenarioID = currentScenarioID
          let conversation: CursorConversationState | undefined
          if (conversationID) {
            conversation = conversations.get(conversationID)
            if (currentScenarioID === AMBIENT_SCENARIO_ID)
              scenarioID = conversation?.scenarioID ?? currentScenarioID
            if (host.hasScenario(scenarioID) && conversation?.scenarioID !== scenarioID) {
              conversation = { scenarioID, messages: [] }
              conversations.set(conversationID, conversation)
            }
          }
          // The record keeps this object, so the rules that a later query states reach it.
          const runWitness = cursorRunRequestWitness(requestFrame)
          const context: ModelRequestContext = {
            // The matcher category is OpenAI Responses. The native request remains protobuf.
            protocol: 'openai-responses',
            path: url.pathname,
            body: { prompt, attachments: cursorAttachmentPayloads(requestFrame), conversationId: conversationID },
            systemText: '',
            userText: prompt,
            scenarioID,
            mockCredential: mockCredentialReceipt(request.headers),
            nativeRequest: runWitness,
            ...(conversationID && conversation ? { serverContext: { conversationId: conversationID, messages: conversation.messages.map(message => ({ ...message })) } } : {}),
          }
          const answer = host.select(context, { allowServiceToolMetadata: true })
          if (answer.kind !== 'step')
            return undefined
          const { step } = answer
          activeAnswer = answer
          answer.recordHttpResponse(response, () => nativeError)
          if (!await answer.holdStep({ request, response }))
            return undefined
          if (step.error)
            return { error: step.error }
          const toolCalls = cursorToolCallsFrom(step.toolCalls)
          if (step.text === undefined && step.reasoning === undefined && toolCalls.length === 0)
            return undefined
          const ownedConversation = conversation
          if (conversationID && ownedConversation) {
            completedTurn = (text) => {
              if (answer.isClosed() || conversations.get(conversationID) !== ownedConversation)
                return
              ownedConversation.messages.push({ role: 'user', content: prompt }, { role: 'assistant', content: text })
            }
          }
          return {
            text: step.text,
            reasoning: step.reasoning,
            toolCalls,
            usage: step.usage,
            ...(runWitness ? { requestContextRules: (rules: readonly CursorContextRule[]) => { runWitness.contextRules = rules.map(rule => ({ ...rule })) } } : {}),
            ...(step.stream ? { stream: answer.stream(response, request) } : {}),
          }
        },
        completed: (_prompt, _frame, text) => completedTurn?.(text),
        errorResponse: error => (nativeError = error),
        holdToolCompletion: (_callID, name) => activeAnswer ? activeAnswer.holdGate(name, { request, response }) : Promise.resolve(false),
      })
      return true
    },
  }
}

/**
 * Answer one Cursor startup call.
 *
 * Model routes receive their native catalog data.
 * Other JSON requests receive {}. Other protobuf requests receive a zero-byte default message.
 *
 * Responses use uncompressed bytes.
 * Without content-encoding, the CLI reads gzip magic as protobuf.
 * The first byte, 0x1f, indicates field 3 with invalid wire type 7.
 */
export function answerCursorStartup(request: IncomingMessage, response: ServerResponse, pathname: string): void {
  const models = modelAnswerFor(pathname)
  if (models) {
    writeCursor(response, 200, { 'content-type': 'application/proto' }, Buffer.from(models))
    return
  }
  const contentType = String(request.headers['content-type'] ?? '')
  if (contentType.includes('json'))
    writeCursor(response, 200, { 'content-type': 'application/json' }, Buffer.from('{}'))
  else
    writeCursor(response, 200, { 'content-type': 'application/proto' }, Buffer.alloc(0))
}

/**
 * Return catalog data for a model route, or undefined for a default startup response.
 *
 * The variant with isDefault selects the initial model from the same catalog.
 * A catalog without that flag selects its first entry.
 * The CLI refuses to start when no model exists.
 */
function modelAnswerFor(pathname: string): Uint8Array | undefined {
  switch (pathname) {
    case CURSOR_AVAILABLE_MODELS_PATH:
      return cursorAvailableModels(CURSOR_MOCK_MODELS)
    case CURSOR_USABLE_MODELS_PATH:
      return cursorUsableModels(CURSOR_MOCK_MODELS)
    case CURSOR_DEFAULT_MODEL_PATH: {
      const marked = CURSOR_MOCK_MODELS.find(model => model.variants.some(variant => variant.isDefault === true))
      const chosen = marked ?? CURSOR_MOCK_MODELS[0]
      return chosen === undefined ? undefined : cursorDefaultModel(chosen)
    }
    default:
      return undefined
  }
}

function writeCursor(response: ServerResponse, status: number, headers: Record<string, string>, body: Buffer): void {
  writeResponseHeaders(response, status, headers)
  response.end(body)
}

/**
 * Build the two native transcript records for one Task call.
 *
 * Cursor stores tool-call on an assistant record and tool-result on a tool record.
 * Both records use the same toolCallId.
 * LeapMux reads arguments from the call and the report that Agent Client Protocol (ACP) omits from the result.
 */
function taskTranscriptRecords(task: CursorScriptedTask): string[] {
  const toolCall = {
    role: 'assistant',
    id: `${task.callID}-call`,
    providerOptions: {},
    content: [{
      type: 'tool-call',
      toolCallId: task.callID,
      toolName: CURSOR_TASK_TOOL,
      args: { description: task.description, prompt: task.prompt },
    }],
  }
  const toolResult = {
    role: 'tool',
    id: `${task.callID}-result`,
    providerOptions: {},
    content: [{
      type: 'tool-result',
      toolCallId: task.callID,
      toolName: CURSOR_TASK_TOOL,
      result: task.report,
      experimental_content: [{ type: 'text', text: task.report }],
    }],
  }
  return [JSON.stringify(toolCall), JSON.stringify(toolResult)]
}

/**
 * Write transcript records through Cursor's KV channel.
 *
 * The blob ID is the SHA-256 of the record. Cursor encodes these 32 bytes as
 * 64 lowercase hex characters in its session database.
 */
function writeTranscriptRecords(response: ServerResponse, records: readonly string[], firstMessageID: number): number {
  let messageID = firstMessageID
  for (const record of records) {
    const data = new TextEncoder().encode(record)
    const blobID = new Uint8Array(createHash('sha256').update(data).digest())
    response.write(Buffer.from(connectFrame(cursorSetBlob(messageID, blobID, data))))
    messageID += 1
  }
  return messageID
}

/** Cursor writes each turn's text to the same KV store as its tool results. */
function textTranscriptRecord(role: 'user' | 'assistant', text: string): string {
  return JSON.stringify({
    role,
    id: randomUUID(),
    providerOptions: {},
    content: [{ type: 'text', text }],
  })
}

/** What a scripted answer supplies for one Cursor turn. */
export interface CursorTurnAnswer {
  error?: MockModelError
  /**
   * The assistant text, delivered as one delta. Absent when the step has none.
   *
   * Explicitly `| undefined`, because `exactOptionalPropertyTypes` otherwise
   * refuses a caller that passes the field through from an optional source.
   */
  text?: string | undefined
  /** Native reasoning text emitted before the final answer. */
  reasoning?: string | undefined
  /** Native calls and queries run before the final text in the same Run stream. */
  toolCalls?: readonly CursorScriptedToolCall[]
  /** Token counts of the scripted turn, sent in the native end update. */
  usage?: MockModelUsage | undefined
  /** Scenario-owned chunk and release controller. */
  stream?: ModelStream
  /** Receive the rules that the CLI states in its answer to a request context query. */
  requestContextRules?: (rules: readonly CursorContextRule[]) => void
}

/** One scripted tool call in Cursor's Run stream. */
export type CursorScriptedToolCall
  = | { kind: 'task', call: CursorTaskCall, report: string, completionGate?: string, taskProgress?: string, nativeExecution?: { modelId: string } }
    | { kind: 'execution', call: CursorExecutionCall }
    | { kind: 'todo', call: CursorTodoCall }
    | { kind: 'generateImage', call: CursorGenerateImageCall }
    | { kind: 'mcp', call: CursorMcpCall }
    | { kind: 'requestContext', callID: string }
    | CursorInteractionCall

interface CursorScriptedTask extends CursorTaskCall {
  report: string
}

/** Decode Cursor's native Todo status words before writing their enum ordinals. */
function cursorTodoStatus(value: unknown): CursorTodoStatus {
  switch (value) {
    case 'TODO_STATUS_PENDING': return 'pending'
    case 'TODO_STATUS_IN_PROGRESS': return 'in_progress'
    case 'TODO_STATUS_COMPLETED': return 'completed'
    case 'TODO_STATUS_CANCELLED': return 'cancelled'
    default: throw new Error(`Cursor Todo has an unsupported status: ${String(value)}`)
  }
}

/** Keep native calls in their scripted order and refuse unknown tool names. */
// A Task can use the remote report path or request actual native child execution.
export function cursorToolCallsFrom(toolCalls: readonly MockModelToolCall[] | undefined): CursorScriptedToolCall[] {
  return (toolCalls ?? []).flatMap((call): CursorScriptedToolCall[] => {
    const args = call.arguments ?? {}
    if (call.name === CURSOR_TASK_TOOL) {
      if (call.nativeExecution && call.taskProgress !== undefined)
        throw new Error('Actual native child execution cannot use scripted child progress.')
      return [{
        kind: 'task',
        call: { callID: call.id, description: String(args.description ?? ''), prompt: String(args.prompt ?? '') },
        report: String(args.report ?? ''),
        ...(call.completionGate !== undefined ? { completionGate: call.completionGate } : {}),
        ...(call.taskProgress !== undefined ? { taskProgress: call.taskProgress } : {}),
        ...(call.nativeExecution !== undefined ? { nativeExecution: call.nativeExecution } : {}),
      }]
    }
    if (call.completionGate !== undefined || call.taskProgress !== undefined || call.nativeExecution !== undefined)
      throw new Error('Only a native Cursor task supports provider-service tool metadata')
    if (call.name === 'shell' || call.name === 'read' || call.name === 'write' || call.name === 'edit')
      return [{ kind: 'execution', call: cursorExecutionCallFrom(call, call.name) }]
    if (call.name === CURSOR_GENERATE_IMAGE_TOOL) {
      if (typeof args.description !== 'string' || typeof args.filePath !== 'string' || typeof args.imageData !== 'string')
        throw new Error('Cursor GenerateImage needs a description, file path, and image data')
      return [{
        kind: 'generateImage',
        call: { callID: call.id, description: args.description, filePath: args.filePath, imageData: args.imageData },
      }]
    }
    if (call.name === CURSOR_QUESTION_TOOL) {
      if (typeof args.title !== 'string' || !Array.isArray(args.questions) || args.questions.length === 0)
        throw new Error('Cursor AskQuestion needs a title and questions')
      const questions = args.questions.map((value, questionIndex) => {
        if (!isObject(value) || typeof value.id !== 'string' || typeof value.prompt !== 'string' || !Array.isArray(value.options))
          throw new Error(`Cursor question ${questionIndex + 1} needs an id, prompt, and options`)
        const options = value.options.map((option, optionIndex) => {
          if (!isObject(option) || typeof option.id !== 'string' || typeof option.label !== 'string')
            throw new Error(`Cursor question option ${optionIndex + 1} needs an id and label`)
          return { id: option.id, label: option.label }
        })
        return { id: value.id, prompt: value.prompt, options, allowMultiple: value.allowMultiple === true }
      })
      return [{ kind: 'question', callID: call.id, title: args.title, questions }]
    }
    if (call.name === CURSOR_CREATE_PLAN_TOOL) {
      if (typeof args.name !== 'string' || typeof args.overview !== 'string' || typeof args.plan !== 'string')
        throw new Error('Cursor CreatePlan needs a name, overview, and plan')
      return [{ kind: 'plan', callID: call.id, name: args.name, overview: args.overview, plan: args.plan }]
    }
    if (call.name === CURSOR_WEB_FETCH_TOOL) {
      if (typeof args.url !== 'string' || !/^https?:\/\//.test(args.url))
        throw new Error('Cursor WebFetch needs an HTTP URL')
      return [{ kind: 'webFetch', callID: call.id, url: args.url }]
    }
    if (call.name === CURSOR_REQUEST_CONTEXT_TOOL)
      return [{ kind: 'requestContext', callID: call.id }]
    if (call.name === CURSOR_MCP_TOOL) {
      if (typeof args.server !== 'string' || !args.server || typeof args.tool !== 'string' || !args.tool || !isObject(args.input))
        throw new Error('Cursor MCP needs a server, tool, and object input')
      return [{ kind: 'mcp', call: { callID: call.id, server: args.server, tool: args.tool, input: args.input } }]
    }
    if (call.name !== 'updateTodos')
      throw new Error(`Cursor mock has no native encoder for ${call.name}`)
    if (!Array.isArray(args.todos))
      throw new Error('Cursor UpdateTodos needs a todos list')
    const todos = args.todos.map((value, index) => {
      if (!isObject(value) || typeof value.content !== 'string')
        throw new Error(`Cursor Todo row ${index + 1} needs content`)
      return {
        id: typeof value.id === 'string' && value.id ? value.id : String(index + 1),
        content: value.content,
        status: cursorTodoStatus(value.status),
      }
    })
    return [{ kind: 'todo', call: { callID: call.id, todos, merge: args.merge === true } }]
  })
}

export interface CursorRunHandlers {
  /**
   * Answer the turn this prompt opens, with its complete Run request frame.
   *
   * Returning undefined closes the stream with no text, which surfaces in the
   * agent as an empty answer rather than as a hang.
   */
  answer: (prompt: string, requestFrame: Uint8Array) => Promise<CursorTurnAnswer | undefined>
  /** Receipt for a successful native turn, after its final text and end update. */
  completed?: (prompt: string, requestFrame: Uint8Array, text: string) => void
  /** The native error encoder supplies the code that this response sends. */
  errorResponse?: (error: MockModelDeliveredError) => void
  holdToolCompletion?: (callId: string, gate: string) => Promise<boolean>
}

function cursorInteractionSummary(call: CursorInteractionCall, reply: CursorInteractionReply): string {
  if (call.kind !== reply.kind)
    throw new Error(`Cursor replied to ${call.kind} with ${reply.kind}`)
  switch (reply.kind) {
    case 'question':
      return reply.rejectedReason
        ? `Cursor question rejected: ${reply.rejectedReason}`
        : `Cursor question selected: ${reply.answers.flatMap(answer => answer.selectedOptionIDs).join(', ') || '(none)'}`
    case 'plan':
      return reply.accepted ? 'Cursor plan accepted' : `Cursor plan rejected: ${reply.error ?? 'no reason'}`
    case 'webFetch':
      return reply.approved ? 'Cursor web fetch approved' : `Cursor web fetch rejected: ${reply.reason ?? 'no reason'}`
  }
}

async function finishCursorTurn(response: ServerResponse, answer: CursorTurnAnswer | undefined, blobMessageID: number, nativeReply?: string): Promise<string | undefined> {
  if (response.destroyed || response.writableEnded)
    return undefined
  const text = [answer?.text, nativeReply].filter((part): part is string => part !== undefined && part !== '').join('\n')
  if (text && !answer?.stream)
    writeTranscriptRecords(response, [textTranscriptRecord('assistant', text)], blobMessageID)
  const stream = answer?.stream ?? createModelStream(response)
  for await (const chunk of stream.chunks(answer?.reasoning))
    response.write(Buffer.from(connectFrame(cursorThinkingDelta(chunk))))
  for await (const chunk of stream.chunks(text || undefined))
    response.write(Buffer.from(connectFrame(cursorTextDelta(chunk))))
  if (!stream.active)
    return undefined
  if (text && answer?.stream)
    writeTranscriptRecords(response, [textTranscriptRecord('assistant', text)], blobMessageID)
  response.write(Buffer.from(connectFrame(cursorTurnEnded(answer?.usage))))
  response.end(Buffer.from(connectEndOfStream()))
  return text
}

/**
 * Serve one agent.v1.AgentService/Run stream.
 *
 * Only the opening Connect frame carries a prompt.
 * Heartbeats and tool results share the stream but contain no prompt.
 * The first frame with a prompt starts the turn and selects its answer.
 */
export async function serveCursorRun(
  request: IncomingMessage,
  response: ServerResponse,
  handlers: CursorRunHandlers,
): Promise<void> {
  writeResponseHeaders(response, 200, { 'content-type': String(request.headers['content-type'] ?? 'application/connect+proto') })
  let pending = Buffer.alloc(0)
  let opening: { prompt: string, frame: Uint8Array } | undefined
  let answer: CursorTurnAnswer | undefined
  let blobMessageID = 1
  let toolIndex = 0
  let nextExecutionId = 300
  let nextInteractionId = 299
  let pendingInteraction: { id: number, call: CursorInteractionCall } | undefined
  let pendingMcp: { id: number } | undefined
  let pendingContext: { id: number } | undefined
  let pendingExecution: CursorExecution | undefined
  let pendingSubagent: { execution: CursorSubagentExecution, callID: string, completionGate?: string } | undefined
  const replies: string[] = []

  const advance = async (): Promise<'waiting' | 'finished'> => {
    const tools = answer?.toolCalls ?? []
    while (toolIndex < tools.length) {
      const tool = tools[toolIndex++]!
      if (response.destroyed || response.writableEnded)
        return 'finished'
      switch (tool.kind) {
        case 'task':
          if (tool.nativeExecution) {
            const parentConversationID = opening && cursorConversationIdOf(opening.frame)
            if (!parentConversationID)
              throw new Error('Actual native Cursor child execution requires its parent conversation ID.')
            const execution = new CursorSubagentExecution({ ...tool.call, modelID: tool.nativeExecution.modelId, parentConversationID }, ++nextExecutionId)
            pendingSubagent = { execution, callID: tool.call.callID, ...(tool.completionGate === undefined ? {} : { completionGate: tool.completionGate }) }
            response.write(Buffer.from(connectFrame(execution.started)))
            response.write(Buffer.from(connectFrame(execution.request())))
            return 'waiting'
          }
          response.write(Buffer.from(connectFrame(cursorTaskStarted(tool.call))))
          if (tool.taskProgress !== undefined)
            response.write(Buffer.from(connectFrame(cursorTaskProgress(tool.call.callID, tool.taskProgress))))
          if (tool.completionGate !== undefined) {
            if (!handlers.holdToolCompletion)
              throw new Error('A native Cursor task completion gate requires a scenario handler')
            if (!await handlers.holdToolCompletion(tool.call.callID, tool.completionGate))
              return 'finished'
          }
          response.write(Buffer.from(connectFrame(cursorTaskCompleted(tool.call, tool.report))))
          blobMessageID = writeTranscriptRecords(response, taskTranscriptRecords({ ...tool.call, report: tool.report }), blobMessageID)
          break
        case 'todo':
          response.write(Buffer.from(connectFrame(cursorTodoStarted(tool.call))))
          response.write(Buffer.from(connectFrame(cursorTodoCompleted(tool.call))))
          break
        case 'generateImage':
          response.write(Buffer.from(connectFrame(cursorGenerateImageStarted(tool.call))))
          response.write(Buffer.from(connectFrame(cursorGenerateImageCompleted(tool.call))))
          break
        case 'execution':
          pendingExecution = new CursorExecution(tool.call, () => ++nextExecutionId)
          response.write(Buffer.from(connectFrame(pendingExecution.started)))
          response.write(Buffer.from(connectFrame(pendingExecution.request())))
          return 'waiting'
        case 'requestContext':
          pendingContext = { id: ++nextExecutionId }
          response.write(Buffer.from(connectFrame(cursorRequestContextExec(pendingContext.id, tool.callID))))
          return 'waiting'
        case 'mcp':
          pendingMcp = { id: ++nextExecutionId }
          response.write(Buffer.from(connectFrame(cursorMcpExec(pendingMcp.id, tool.call))))
          return 'waiting'
        case 'question':
        case 'plan':
        case 'webFetch':
          pendingInteraction = { id: ++nextInteractionId, call: tool }
          response.write(Buffer.from(connectFrame(cursorInteractionQuery(pendingInteraction.id, tool))))
          return 'waiting'
      }
    }
    const text = await finishCursorTurn(response, answer, blobMessageID, replies.length > 0 ? replies.join('\n') : undefined)
    if (answer && opening && text !== undefined)
      handlers.completed?.(opening.prompt, opening.frame, text)
    return 'finished'
  }

  for await (const chunk of request) {
    pending = Buffer.concat([pending, Buffer.from(chunk)])
    const { frames, rest } = takeConnectFrames(new Uint8Array(pending))
    pending = Buffer.from(rest)
    for (const encoded of frames) {
      if ((encoded.flags & ~0x03) !== 0)
        throw new Error('The native Cursor request has unsupported Connect flags.')
      if ((encoded.flags & 0x02) !== 0)
        continue
      const compressed = (encoded.flags & 0x01) !== 0
      const encoding = request.headers['connect-content-encoding']
      if (compressed && (typeof encoding !== 'string' || encoding.trim().toLowerCase() !== 'gzip'))
        throw new Error('The native Cursor compressed request requires gzip encoding.')
      const frame = compressed ? new Uint8Array(gunzipSync(encoded.payload, { maxOutputLength: MAX_MOCK_REQUEST_BYTES })) : encoded.payload
      if (pendingSubagent) {
        const result = pendingSubagent.execution.acceptReply(frame)
        if (!result)
          continue
        if (pendingSubagent.completionGate !== undefined) {
          if (!handlers.holdToolCompletion)
            throw new Error('The native Cursor child completion requires a scenario gate controller.')
          if (!await handlers.holdToolCompletion(pendingSubagent.callID, pendingSubagent.completionGate))
            return
        }
        response.write(Buffer.from(connectFrame(result.update)))
        replies.push(result.reply.success ? result.reply.finalMessage ?? '' : result.reply.error)
        pendingSubagent = undefined
        if (await advance() === 'finished')
          return
        continue
      }
      if (pendingExecution) {
        const result = pendingExecution.acceptReply(frame)
        if (!result)
          continue
        if (result.state === 'request') {
          response.write(Buffer.from(connectFrame(result.request)))
          continue
        }
        response.write(Buffer.from(connectFrame(result.update)))
        replies.push(result.receipt.text)
        pendingExecution = undefined
        if (await advance() === 'finished')
          return
        continue
      }
      if (pendingContext) {
        const reply = cursorRequestContextResponseOf(frame)
        if (!reply)
          continue
        if (reply.id !== pendingContext.id)
          throw new Error(`Cursor replied to request context query ${pendingContext.id} with id ${reply.id}`)
        if (!answer?.requestContextRules)
          throw new Error('A native Cursor request context query requires a scenario handler')
        answer.requestContextRules(reply.rules)
        pendingContext = undefined
        if (await advance() === 'finished')
          return
        continue
      }
      if (pendingMcp) {
        const reply = cursorMcpResponseOf(frame)
        if (!reply)
          continue
        if (reply.id !== pendingMcp.id)
          throw new Error(`Cursor replied to MCP execution ${pendingMcp.id} with id ${reply.id}`)
        replies.push(reply.success ? reply.text : `Cursor MCP failed: ${reply.text}`)
        pendingMcp = undefined
        if (await advance() === 'finished')
          return
        continue
      }
      if (pendingInteraction) {
        const reply = cursorInteractionResponseOf(frame)
        if (!reply)
          continue
        if (reply.id !== pendingInteraction.id)
          throw new Error(`Cursor replied to query ${pendingInteraction.id} with id ${reply.id}`)
        replies.push(cursorInteractionSummary(pendingInteraction.call, reply))
        pendingInteraction = undefined
        if (await advance() === 'finished')
          return
        continue
      }
      const prompt = cursorPromptOf(frame)
      if (prompt === undefined || opening)
        continue
      opening = { prompt, frame }
      answer = await handlers.answer(prompt, frame)
      if (response.destroyed || response.writableEnded)
        return
      if (answer?.error) {
        const native = cursorErrorResponse(answer.error)
        handlers.errorResponse?.(native.error)
        response.end(Buffer.from(native.frame))
        return
      }
      // The native store needs the user blob before any tool or text update.
      blobMessageID = writeTranscriptRecords(response, [textTranscriptRecord('user', prompt)], 1)
      if (await advance() === 'finished')
        return
    }
  }
  // An unanswered native request creates no completed-turn receipt.
  if (!response.destroyed && !response.writableEnded)
    response.end(Buffer.from(connectEndOfStream()))
}
