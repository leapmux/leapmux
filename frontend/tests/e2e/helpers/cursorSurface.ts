/**
 * The Cursor half of the mock endpoint.
 *
 * `cursor-agent` speaks neither the OpenAI nor the Anthropic model API. It talks
 * to its own backend, so this module answers that surface instead of a model
 * protocol, and hands the prompt it decodes to the same scenario machinery every
 * other provider uses. See `./cursorWire` for the wire format.
 *
 * THREE FACTS DECIDE THE SHAPE HERE, and each cost a probe run to establish.
 *
 * The startup calls answer ALL-DEFAULTS, not real data. A zero-byte protobuf
 * body IS a valid message -- every field takes its default -- and that is what
 * keeps the agent pointed at this endpoint. A proxy that forwarded those twenty
 * calls to the real backend produced a correct answer and then never sent the
 * turn here at all: no response body carries a host name, so a REAL
 * `GetServerConfig` switches the agent to its built-in endpoint.
 *
 * The THREE model calls are the exception, and each must answer real data.
 * `AvailableModels` supplies the catalogue: an all-defaults answer is an empty
 * model list, `session/new` then reports `availableModels: []`, and LeapMux has
 * no model to start a turn with. Nothing reports this, because the CLI CATCHES
 * the failure and substitutes an empty list -- so a mis-encoded body and an
 * honestly empty catalogue look identical from outside. `GetDefaultModelForCli`
 * and `GetUsableModels` then say which entry to begin on; without them the agent
 * refuses `session/new` with "No model found. Please check your model settings."
 *
 * The turn arrives over HTTP/2 while everything else is HTTP/1.1, on the same
 * port. The caller supplies a listener that serves both; see
 * `createDualVersionListener`.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { CursorGenerateImageCall, CursorInteractionCall, CursorInteractionReply, CursorMcpCall, CursorModel, CursorTaskCall, CursorTodoCall, CursorTodoStatus } from './cursorWire'
import type { MockModelToolCall, MockModelUsage } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { createHash, randomUUID } from 'node:crypto'
import { isObject } from '../../../src/lib/jsonPick'
import {
  connectEndOfStream,
  connectFrame,
  cursorAvailableModels,
  cursorDefaultModel,
  cursorGenerateImageCompleted,
  cursorGenerateImageStarted,
  cursorInteractionQuery,
  cursorInteractionResponseOf,
  cursorMcpExec,
  cursorMcpResponseOf,
  cursorPromptOf,
  cursorSetBlob,
  cursorTaskCompleted,
  cursorTaskStarted,
  cursorTextDelta,
  cursorThinkingDelta,
  cursorTodoCompleted,
  cursorTodoStarted,
  cursorTurnEnded,
  cursorUsableModels,
  takeConnectFrames,
} from './cursorWire'

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
    variants: [{ id: 'default[]', displayName: 'Auto', isDefault: true }],
  },
  {
    name: 'mock-sonnet',
    displayName: 'Mock Sonnet',
    variants: [
      { id: 'mock-sonnet[context=200k,effort=low]', displayName: 'Mock Sonnet Low' },
      { id: 'mock-sonnet[context=200k,effort=high]', displayName: 'Mock Sonnet High' },
    ],
  },
  {
    name: 'mock-grok',
    displayName: 'Mock Grok',
    variants: [
      { id: 'mock-grok[context=256k,reasoning_effort=low]', displayName: 'Mock Grok Low' },
      { id: 'mock-grok[context=256k,reasoning_effort=xhigh]', displayName: 'Mock Grok Extra High' },
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

/**
 * Answer one of Cursor's startup calls.
 *
 * `AvailableModels` takes the catalogue. A JSON request takes `{}` and
 * everything else takes a zero-byte protobuf body; neither states anything,
 * which is the point. See the note at the top for both halves.
 *
 * The body goes back UNCOMPRESSED. The real backend gzips its larger answers,
 * and a recording replayed with those bytes but no `content-encoding` made the
 * client read gzip magic as protobuf -- `0x1f` parses as field 3, wire type 7,
 * which is not a wire type at all.
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
 * The model answer this path takes, or undefined when it takes an all-defaults one.
 *
 * The default model is the one holding the variant marked `isDefault`, so the
 * catalogue and the starting selection cannot name different models. A catalogue
 * that marks none falls back to its first entry rather than to no model, because
 * "no model" is the state the agent refuses to start in.
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
  response.writeHead(status, headers)
  response.end(body)
}

/**
 * The two transcript records one Task call leaves in the session store.
 *
 * Their shape is Cursor's own, read off a real store: a `tool-call` block on an
 * ASSISTANT record and a `tool-result` block on a TOOL record, matched by
 * `toolCallId`. LeapMux reads the result block for the report that ACP omits,
 * and the call block for the arguments.
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
}

/** One scripted tool call in Cursor's Run stream. */
export type CursorScriptedToolCall
  = | { kind: 'task', call: CursorTaskCall, report: string }
    | { kind: 'todo', call: CursorTodoCall }
    | { kind: 'generateImage', call: CursorGenerateImageCall }
    | { kind: 'mcp', call: CursorMcpCall }
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
// Cursor resolves a child locally. Its report rides inside the scripted Task call
// because the CLI never asks the endpoint for a separate child turn.
export function cursorToolCallsFrom(toolCalls: readonly MockModelToolCall[] | undefined): CursorScriptedToolCall[] {
  return (toolCalls ?? []).flatMap((call): CursorScriptedToolCall[] => {
    const args = call.arguments ?? {}
    if (call.name === CURSOR_TASK_TOOL) {
      return [{
        kind: 'task',
        call: { callID: call.id, description: String(args.description ?? ''), prompt: String(args.prompt ?? '') },
        report: String(args.report ?? ''),
      }]
    }
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

function finishCursorTurn(response: ServerResponse, answer: CursorTurnAnswer | undefined, blobMessageID: number, nativeReply?: string): void {
  const text = [answer?.text, nativeReply].filter((part): part is string => part !== undefined && part !== '').join('\n')
  if (text)
    writeTranscriptRecords(response, [textTranscriptRecord('assistant', text)], blobMessageID)
  if (answer?.reasoning)
    response.write(Buffer.from(connectFrame(cursorThinkingDelta(answer.reasoning))))
  if (text)
    response.write(Buffer.from(connectFrame(cursorTextDelta(text))))
  response.write(Buffer.from(connectFrame(cursorTurnEnded(answer?.usage))))
  response.end(Buffer.from(connectEndOfStream()))
}

/**
 * Serve one `agent.v1.AgentService/Run` stream.
 *
 * The client sends Connect frames as it goes, and only the frame that OPENS the
 * turn carries a prompt -- a heartbeat and a tool result travel on the same
 * stream and decode to no prompt at all. The first frame that yields one is
 * therefore the turn, and the answer follows it.
 */
export async function serveCursorRun(
  request: IncomingMessage,
  response: ServerResponse,
  handlers: CursorRunHandlers,
): Promise<void> {
  response.writeHead(200, { 'content-type': String(request.headers['content-type'] ?? 'application/connect+proto') })
  let pending = Buffer.alloc(0)
  let answered = false
  let pendingInteraction: { id: number, call: CursorInteractionCall, answer: CursorTurnAnswer | undefined, blobMessageID: number } | undefined
  let pendingMcp: { id: number, answer: CursorTurnAnswer | undefined, blobMessageID: number } | undefined

  for await (const chunk of request) {
    pending = Buffer.concat([pending, Buffer.from(chunk)])
    const { frames, rest } = takeConnectFrames(new Uint8Array(pending))
    pending = Buffer.from(rest)
    for (const frame of frames) {
      if (pendingMcp) {
        const reply = cursorMcpResponseOf(frame)
        if (!reply)
          continue
        if (reply.id !== pendingMcp.id)
          throw new Error(`Cursor replied to MCP execution ${pendingMcp.id} with id ${reply.id}`)
        const result = reply.success ? reply.text : `Cursor MCP failed: ${reply.text}`
        finishCursorTurn(response, pendingMcp.answer, pendingMcp.blobMessageID, result)
        return
      }
      if (pendingInteraction) {
        const reply = cursorInteractionResponseOf(frame)
        if (!reply)
          continue
        if (reply.id !== pendingInteraction.id)
          throw new Error(`Cursor replied to query ${pendingInteraction.id} with id ${reply.id}`)
        finishCursorTurn(response, pendingInteraction.answer, pendingInteraction.blobMessageID, cursorInteractionSummary(pendingInteraction.call, reply))
        return
      }
      const prompt = cursorPromptOf(frame)
      if (prompt === undefined || answered)
        continue
      answered = true
      const answer = await handlers.answer(prompt, frame)
      // Cursor's session/load requires store.db. Text updates alone do not create it.
      let blobMessageID = writeTranscriptRecords(response, [textTranscriptRecord('user', prompt)], 1)
      // A tool call goes out BEFORE the text, in the order a real turn uses:
      // the agent opens the row, runs the child, then speaks.
      let interaction: CursorInteractionCall | undefined
      let mcp: CursorMcpCall | undefined
      for (const tool of answer?.toolCalls ?? []) {
        if (tool.kind === 'task') {
          response.write(Buffer.from(connectFrame(cursorTaskStarted(tool.call))))
          response.write(Buffer.from(connectFrame(cursorTaskCompleted(tool.call, tool.report))))
          // The updates above draw the row; these WRITE the transcript, which is
          // the only place the child's report survives. See `cursorSetBlob`.
          blobMessageID = writeTranscriptRecords(response, taskTranscriptRecords({ ...tool.call, report: tool.report }), blobMessageID)
        }
        else if (tool.kind === 'todo') {
          response.write(Buffer.from(connectFrame(cursorTodoStarted(tool.call))))
          response.write(Buffer.from(connectFrame(cursorTodoCompleted(tool.call))))
        }
        else if (tool.kind === 'generateImage') {
          response.write(Buffer.from(connectFrame(cursorGenerateImageStarted(tool.call))))
          response.write(Buffer.from(connectFrame(cursorGenerateImageCompleted(tool.call))))
        }
        else if (tool.kind === 'mcp') {
          if (interaction || mcp)
            throw new Error('Cursor mock supports one native request per turn')
          mcp = tool.call
        }
        else if (interaction || mcp) {
          throw new Error('Cursor mock supports one native request per turn')
        }
        else {
          interaction = tool
        }
      }
      if (mcp) {
        const id = 301
        response.write(Buffer.from(connectFrame(cursorMcpExec(id, mcp))))
        pendingMcp = { id, answer, blobMessageID }
        continue
      }
      if (interaction) {
        const id = 300
        response.write(Buffer.from(connectFrame(cursorInteractionQuery(id, interaction))))
        pendingInteraction = { id, call: interaction, answer, blobMessageID }
        continue
      }
      finishCursorTurn(response, answer, blobMessageID)
      return
    }
  }
  // The client closed without ever opening a turn. Close the stream cleanly so
  // it reads an empty answer rather than a transport fault.
  if (!answered || pendingInteraction || pendingMcp)
    response.end(Buffer.from(connectEndOfStream()))
}
