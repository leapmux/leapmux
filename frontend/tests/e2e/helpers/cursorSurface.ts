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
import type { CursorModel, CursorTaskCall } from './cursorWire'
import type { MockModelToolCall } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import {
  connectEndOfStream,
  connectFrame,
  cursorAvailableModels,
  cursorDefaultModel,
  cursorPromptOf,
  cursorSetBlob,
  cursorTaskCompleted,
  cursorTaskStarted,
  cursorTextDelta,
  cursorTurnEnded,
  cursorUsableModels,
  takeConnectFrames,
} from './cursorWire'

/** The bidirectional stream that carries one whole Cursor turn. */
export const CURSOR_RUN_PATH = '/agent.v1.AgentService/Run'

/** Cursor's subagent tool, whose calls this surface turns into Run-stream updates. */
export const CURSOR_TASK_TOOL = 'task'

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
 * Write one task's transcript records over the KV channel.
 *
 * The blob id is the SHA-256 of the record, which is how Cursor's own ids read:
 * content-addressed, and 64 hex characters once the client encodes these bytes.
 */
function writeTaskTranscript(response: ServerResponse, task: CursorScriptedTask, firstMessageID: number): number {
  let messageID = firstMessageID
  for (const record of taskTranscriptRecords(task)) {
    const data = new TextEncoder().encode(record)
    const blobID = new Uint8Array(createHash('sha256').update(data).digest())
    response.write(Buffer.from(connectFrame(cursorSetBlob(messageID, blobID, data))))
    messageID += 1
  }
  return messageID
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
  /**
   * The Task tool calls to run before the text.
   *
   * Each one opens and closes in the same turn, which is what a foreground
   * subagent does. The report is the child's answer, and the agent puts it in
   * the row and in the child transcript.
   */
  taskCalls?: readonly CursorScriptedTask[]
}

/** One scripted Task tool call, with the report its child returns. */
export interface CursorScriptedTask extends CursorTaskCall {
  report: string
}

/**
 * The Task calls inside one scripted step, in Cursor's own wire shape.
 *
 * Cursor resolves a subagent LOCALLY: the CLI never asks the endpoint for the
 * child's turn, so unlike every other provider the child's answer cannot come
 * from a rule. The scripted call carries it as a `report` argument instead, and
 * this is where that convention is read.
 */
export function cursorTaskCallsFrom(toolCalls: readonly MockModelToolCall[] | undefined): CursorScriptedTask[] {
  return (toolCalls ?? [])
    .filter(call => call.name === CURSOR_TASK_TOOL)
    .map((call) => {
      const args = call.arguments ?? {}
      return {
        callID: call.id,
        description: String(args.description ?? ''),
        prompt: String(args.prompt ?? ''),
        report: String(args.report ?? ''),
      }
    })
}

export interface CursorRunHandlers {
  /**
   * Answer the turn this prompt opens, or report that nothing scripted it.
   *
   * Returning undefined closes the stream with no text, which surfaces in the
   * agent as an empty answer rather than as a hang.
   */
  answer: (prompt: string) => Promise<CursorTurnAnswer | undefined>
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

  for await (const chunk of request) {
    pending = Buffer.concat([pending, Buffer.from(chunk)])
    const { frames, rest } = takeConnectFrames(new Uint8Array(pending))
    pending = Buffer.from(rest)
    for (const frame of frames) {
      const prompt = cursorPromptOf(frame)
      if (prompt === undefined || answered)
        continue
      answered = true
      const answer = await handlers.answer(prompt)
      // A tool call goes out BEFORE the text, in the order a real turn uses:
      // the agent opens the row, runs the child, then speaks.
      let blobMessageID = 1
      for (const task of answer?.taskCalls ?? []) {
        response.write(Buffer.from(connectFrame(cursorTaskStarted(task))))
        response.write(Buffer.from(connectFrame(cursorTaskCompleted(task, task.report))))
        // The updates above draw the row; these WRITE the transcript, which is
        // the only place the child's report survives. See `cursorSetBlob`.
        blobMessageID = writeTaskTranscript(response, task, blobMessageID)
      }
      if (answer?.text !== undefined && answer.text !== '')
        response.write(Buffer.from(connectFrame(cursorTextDelta(answer.text))))
      response.write(Buffer.from(connectFrame(cursorTurnEnded())))
      response.end(Buffer.from(connectEndOfStream()))
      return
    }
  }
  // The client closed without ever opening a turn. Close the stream cleanly so
  // it reads an empty answer rather than a transport fault.
  if (!answered)
    response.end(Buffer.from(connectEndOfStream()))
}
