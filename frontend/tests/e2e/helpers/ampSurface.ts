/**
 * The Amp half of the mock endpoint.
 *
 * Amp's CLI speaks no model API. It talks to Amp's own service: a few REST calls,
 * and a JSON-RPC WebSocket to the thread's ACTOR, which runs the agent loop on the
 * server and leases each local tool call back to the CLI's executor. So this module
 * plays that service, and it runs the agent loop itself: each inference the loop
 * needs becomes one request to the scenario machinery every other provider uses.
 *
 * FOUR FACTS DECIDE THE SHAPE HERE, and each cost a probe run of the real CLI to
 * establish.
 *
 * The actor sends an unsolicited `pong` text frame the moment a socket opens, and
 * the CLI reads the first server frame as the readiness signal. A socket that stays
 * silent never gets its first request.
 *
 * The protocol validates ids: a message id is `M-` and a tool-use id is `TU-`, each
 * followed by exactly 22 base62 characters. A frame with any other id fails to
 * decode, and the CLI drops it without a word -- the turn then simply never ends.
 *
 * The CLI opens TWO sockets per thread: the thread client and the executor. The
 * service broadcasts every thread notification to both, and sends a tool lease to
 * the executor alone, which is the socket that sent `executor_connect`.
 *
 * The executor runs a lease under the tool's EXECUTOR name, which differs from the
 * model's name for one tool: `shell_command` runs as `async_shell_command`.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Duplex } from 'node:stream'
import type { MockModelStep, MockModelToolCall } from './mockModelScript'
import type { WebSocketConnection } from './webSocketServer'
import { Buffer } from 'node:buffer'
import { createHash, randomBytes, randomUUID } from 'node:crypto'
// RELATIVE imports, not `~/...`. See the note in `../agentSettings.ts`.
import { AMP_TOOL_NAME } from '../../../src/components/chat/providers/amp/toolNames'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '../../../src/generated/contracts/amp-protocol'
import { acceptWebSocket } from './webSocketServer'

/** The path prefix of the actor gateway, which the CLI derives from `RIVET_PUBLIC_ENDPOINT`. */
export const AMP_ACTOR_PATH_PREFIX = '/actors/'

/** The REST prefix of Amp's service. No other provider the mock serves uses it. */
const AMP_API_PREFIX = '/api/'

/** The test-only view of the mock's threads. */
export const AMP_E2E_THREADS_PATH = '/__e2e/amp/threads'

/** The subprotocol the actor gateway answers with. */
const RIVET_PROTOCOL = 'rivet'

/** The tools that Amp runs on its SERVER as a subagent. The mock answers each with a child inference. */
export const AMP_SERVER_SUBAGENT_TOOLS: ReadonlySet<string> = new Set(Object.values(AMP_SUBAGENT_TOOL))

/**
 * The executor's name for a tool whose name differs from the model's.
 *
 * Every other local tool runs under the name the model called it by.
 */
const EXECUTOR_TOOL_NAMES: ReadonlyMap<string, string> = new Map([[AMP_SHELL_TOOL.ShellCommand, AMP_TOOL_NAME.AsyncShellCommand]])

/** The mode a thread starts in when its creation states none. Amp's own default. */
const DEFAULT_AGENT_MODE = 'medium'

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'

/** 22 base62 characters from the given bytes. */
function base62(bytes: Uint8Array): string {
  let out = ''
  for (let index = 0; index < 22; index++)
    out += BASE62[bytes[index % bytes.length]! % 62]
  return out
}

/** A fresh message id, `M-` and 22 base62 characters. */
export function ampMessageID(): string {
  return `M-${base62(randomBytes(22))}`
}

/**
 * The tool-use id of one scripted call, `TU-` and 22 base62 characters.
 *
 * Derived from the thread and the script's own id, so a call reads the same in every
 * log of one run.
 */
export function ampToolUseID(threadID: string, scriptedID: string): string {
  return `TU-${base62(createHash('sha256').update(`${threadID}\0${scriptedID}`).digest())}`
}

/** Whether a request belongs to Amp's service. */
export function isAmpPath(pathname: string): boolean {
  return pathname.startsWith(AMP_API_PREFIX) || pathname.startsWith(AMP_ACTOR_PATH_PREFIX) || pathname === AMP_E2E_THREADS_PATH
}

/** One model request the agent loop needs answered. */
export interface AmpInference {
  /** The conversation, in the Anthropic Messages shape the scenario matchers read. */
  body: { model: string, system: string, messages: Record<string, unknown>[] }
  /** The text of the last user turn. */
  userText: string
}

export interface AmpSurfaceOptions {
  /**
   * Answer one inference, or report that nothing scripted it.
   *
   * Undefined makes the turn fail the way Amp's own service fails one: an error that
   * ends the CLI process with the reason.
   */
  answer: (inference: AmpInference) => Promise<MockModelStep | undefined>
}

/** One thread, as a test reads it back. */
export interface AmpThreadView {
  id: string
  agentMode: string
  tree: string
  title: string
  messageCount: number
  archived: boolean
}

/** A thread a test seeds, which no process of the run created. */
export interface AmpSeededThread {
  id: string
  title: string
  tree: string
  messageCount: number
  archived?: boolean
  /** Milliseconds since the epoch. Defaults to now. */
  updatedAt?: number
}

export interface AmpSurface {
  handleHttp: (request: IncomingMessage, response: ServerResponse, url: URL) => Promise<void>
  handleUpgrade: (request: IncomingMessage, socket: Duplex, head: Buffer, url: URL) => void
  close: () => void
}

/** One content block of a thread message, in the actor's own shape. */
type ActorBlock = Record<string, unknown>

interface ActorMessage {
  role: 'user' | 'assistant'
  content: ActorBlock[]
  messageId: string
}

interface ThreadSocket {
  connection: WebSocketConnection
  executor: boolean
}

interface RunningTurn {
  cancelled: boolean
  /** Settles every wait of the turn, so a cancel ends the loop at its next step. */
  abort: AbortController
}

interface ActorThread {
  id: string
  agentMode: string
  seq: number
  messages: ActorMessage[]
  tree: string
  title: string
  archived: boolean
  updatedAt: number
  seededMessageCount: number
  sockets: Set<ThreadSocket>
  turn: RunningTurn | undefined
  /** Steering messages that arrived during the turn, for its next interruption point. */
  steers: ActorBlock[][]
  /** The executor's answer to each lease that waits for one. */
  leases: Map<string, (run: unknown) => void>
}

const MOCK_USER = { id: 'user_leapmux_e2e', email: 'e2e@leapmux.invalid', username: 'leapmux-e2e', displayName: 'LeapMux E2E', features: [], team: null }

/** Build the Amp service. */
export function createAmpSurface(options: AmpSurfaceOptions): AmpSurface {
  const threads = new Map<string, ActorThread>()

  function threadFor(id: string, agentMode?: string): ActorThread {
    let thread = threads.get(id)
    if (!thread) {
      thread = {
        id,
        agentMode: agentMode || DEFAULT_AGENT_MODE,
        seq: 0,
        messages: [],
        tree: '',
        title: '',
        archived: false,
        updatedAt: Date.now(),
        seededMessageCount: 0,
        sockets: new Set(),
        turn: undefined,
        steers: [],
        leases: new Map(),
      }
      threads.set(id, thread)
    }
    return thread
  }

  async function handleHttp(request: IncomingMessage, response: ServerResponse, url: URL): Promise<void> {
    if (url.pathname === AMP_E2E_THREADS_PATH) {
      if (request.method === 'POST') {
        const seeded = await readJSON(request) as AmpSeededThread
        const thread = threadFor(seeded.id)
        thread.title = seeded.title
        thread.tree = seeded.tree
        thread.seededMessageCount = seeded.messageCount
        thread.archived = seeded.archived === true
        thread.updatedAt = seeded.updatedAt ?? Date.now()
        writeJSON(response, 201, viewOf(thread))
        return
      }
      writeJSON(response, 200, [...threads.values()].map(viewOf))
      return
    }
    if (url.pathname === '/api/thread-actors' && request.method === 'POST') {
      const body = await readJSON(request)
      const requested = isRecord(body) && typeof body.threadId === 'string' ? body.threadId : ''
      const thread = threadFor(requested || `T-${randomUUID()}`, isRecord(body) && typeof body.agentMode === 'string' ? body.agentMode : undefined)
      writeJSON(response, 200, {
        threadId: thread.id,
        wsToken: 'leapmux-e2e-ws-token',
        ownerUserId: MOCK_USER.id,
        threadVersion: thread.seq,
        poolName: 'leapmux-e2e',
        usesDtw: true,
        usesThreadActors: true,
        executorType: 'local-client',
        agentMode: thread.agentMode,
      })
      return
    }
    if (url.pathname === '/api/internal') {
      const body = request.method === 'GET' ? {} : await readJSON(request).catch(() => ({}))
      const method = [...url.searchParams.keys()][0] ?? ''
      const params = isRecord(body) && isRecord(body.params) ? body.params : {}
      writeJSON(response, 200, internalCall(method, params))
      return
    }
    if (url.pathname === '/api/telemetry') {
      writeJSON(response, 200, { ok: true })
      return
    }
    writeJSON(response, 404, { error: `The Amp mock has no route for ${request.method ?? 'UNKNOWN'} ${url.pathname}` })
  }

  function internalCall(method: string, params: Record<string, unknown>): unknown {
    switch (method) {
      case 'getUserInfo':
        return { ok: true, result: MOCK_USER }
      case 'loadPlugins':
        return { ok: true, result: [] }
      case 'loadSkills':
        return { ok: true, result: { sources: [] } }
      case 'listThreads': {
        // Amp's own list keeps an archived thread out, and so does this one.
        const listed = [...threads.values()]
          .filter(thread => !thread.archived && messageCount(thread) > 0)
          .sort((a, b) => b.updatedAt - a.updatedAt)
          .map(thread => ({
            id: thread.id,
            title: thread.title || null,
            userLastInteractedAt: thread.updatedAt,
            env: { initial: { trees: thread.tree ? [{ uri: thread.tree, displayName: thread.tree.split('/').at(-1) ?? '' }] : [] } },
            messageCount: messageCount(thread),
            meta: { visibility: 'private' },
          }))
        const offset = typeof params.offset === 'number' ? params.offset : 0
        const limit = typeof params.limit === 'number' ? params.limit : 200
        return { ok: true, result: { threads: listed.slice(offset, offset + limit) } }
      }
      case 'getThreadTail': {
        const thread = threadFor(typeof params.thread === 'string' ? params.thread : `T-${randomUUID()}`)
        return {
          ok: true,
          result: {
            thread: { data: { id: thread.id, v: thread.seq, created: thread.updatedAt, agentMode: thread.agentMode, meta: { agentMode: thread.agentMode, executorType: 'local-client', usesDtw: true, usesThreadActors: true, visibility: 'private' } } },
            messages: [],
            hasMoreBefore: false,
          },
        }
      }
      case 'archiveThread': {
        const id = typeof params.thread === 'string' ? params.thread : typeof params.threadID === 'string' ? params.threadID : ''
        const thread = threads.get(id)
        if (thread)
          thread.archived = true
        return { ok: true, result: {} }
      }
      default:
        return { ok: true, result: {} }
    }
  }

  function handleUpgrade(request: IncomingMessage, socket: Duplex, head: Buffer, url: URL): void {
    const threadID = url.searchParams.get('rvt-key') ?? ''
    if (!url.pathname.startsWith(AMP_ACTOR_PATH_PREFIX) || threadID === '') {
      socket.end('HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n')
      return
    }
    const connection = acceptWebSocket(request, socket, head, { protocols: [RIVET_PROTOCOL] })
    if (!connection)
      return
    const thread = threadFor(threadID)
    const member: ThreadSocket = { connection, executor: false }
    thread.sockets.add(member)
    connection.onClose(() => thread.sockets.delete(member))
    connection.onMessage((text) => {
      void handleFrame(thread, member, text).catch((error: unknown) => {
        console.error('[amp mock] frame failed', error)
      })
    })
    // The readiness signal; see the note at the top.
    connection.send('pong')
  }

  async function handleFrame(thread: ActorThread, member: ThreadSocket, text: string): Promise<void> {
    if (text === 'ping') {
      member.connection.send('pong')
      return
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    }
    catch {
      return
    }
    for (const message of Array.isArray(parsed) ? parsed : [parsed]) {
      if (isRecord(message))
        handleRequest(thread, member, message)
    }
  }

  function handleRequest(thread: ActorThread, member: ThreadSocket, message: Record<string, unknown>): void {
    const params = isRecord(message.params) ? message.params : {}
    const reply = (result: unknown) => {
      if (message.id !== undefined)
        member.connection.send(JSON.stringify({ jsonrpc: '2.0', id: message.id, result }))
    }
    switch (message.method) {
      case 'executor_connect':
        member.executor = true
        reply({ ok: true })
        notify(thread, 'executor_connected', { executorId: params.clientId, registeredToolCount: 0, guidanceInventory: [], resumeBootstrap: false, executorSystemInfo: true })
        return
      case 'executor_environment_snapshot': {
        const environment = isRecord(params.environment) ? params.environment : {}
        const trees = Array.isArray(environment.trees) ? environment.trees : []
        const first = trees.find(isRecord)
        if (first && typeof first.uri === 'string')
          thread.tree = first.uri
        reply({ ok: true })
        return
      }
      case 'client_resume':
        member.connection.send(JSON.stringify([
          { type: 'agent_state', state: thread.turn ? 'working' : 'idle', agentMode: thread.agentMode },
          { type: 'thread_settings', settings: {} },
          { type: 'thread_features', features: [] },
          { type: 'queued_messages', messages: [], openConversationBatchId: null },
          { type: 'tool_approval_queue', approvals: [] },
        ]))
        reply({ replayMessageCount: 0, storedEventCount: 0, replayThroughSeq: thread.seq })
        return
      case 'client_append_user_msg': {
        const content = Array.isArray(params.content) ? params.content.filter(isRecord) : []
        reply({ ok: true })
        if (thread.turn) {
          // A message during a turn waits for the turn's next interruption point, as
          // Amp's own queue does. LeapMux sends one only as a steering line.
          thread.steers.push(content)
          return
        }
        if (!thread.title)
          thread.title = firstText(content).slice(0, 80)
        addMessage(thread, { role: 'user', content, messageId: typeof params.messageId === 'string' ? params.messageId : ampMessageID() })
        void runTurn(thread)
        return
      }
      case 'executor_tool_result': {
        reply({ ok: true })
        const callID = typeof params.toolCallId === 'string' ? params.toolCallId : ''
        notify(thread, 'executor_tool_result_ack', { toolCallId: callID })
        const waiter = thread.leases.get(callID)
        if (waiter) {
          thread.leases.delete(callID)
          waiter(params.run)
        }
        return
      }
      case 'client_cancel':
        reply({ ok: true })
        cancelTurn(thread)
        return
      default:
        reply({ ok: true })
    }
  }

  function notify(thread: ActorThread, method: string, params: Record<string, unknown>): void {
    const frame = JSON.stringify({ jsonrpc: '2.0', method, params })
    for (const member of thread.sockets)
      member.connection.send(frame)
  }

  function addMessage(thread: ActorThread, message: ActorMessage & { usage?: Record<string, unknown>, state?: Record<string, unknown> }): void {
    thread.seq += 1
    thread.updatedAt = Date.now()
    thread.messages.push({ role: message.role, content: message.content, messageId: message.messageId })
    notify(thread, 'message_added', {
      message: { threadId: thread.id, readAt: null, createdAt: new Date().toISOString(), ...message },
      seq: thread.seq,
    })
  }

  function cancelTurn(thread: ActorThread): void {
    const turn = thread.turn
    if (!turn)
      return
    turn.cancelled = true
    turn.abort.abort()
    thread.turn = undefined
    thread.steers = []
    thread.leases.clear()
    notify(thread, 'agent_state', { state: 'idle', agentMode: thread.agentMode })
  }

  /**
   * Fail the turn the way Amp's service does: one error that ends the CLI process.
   *
   * A failed turn reaches no interruption point, so its steering lines end with it,
   * as they do for a cancel. A line that outlived the turn would continue the NEXT
   * turn with an inference that no test scripted.
   */
  function failTurn(thread: ActorThread, message: string, code: string): void {
    thread.seq += 1
    notify(thread, 'error_set', { seq: thread.seq, error: { message, code } })
    thread.turn = undefined
    thread.steers = []
    notify(thread, 'agent_state', { state: 'idle', agentMode: thread.agentMode })
  }

  async function runTurn(thread: ActorThread): Promise<void> {
    const turn: RunningTurn = { cancelled: false, abort: new AbortController() }
    thread.turn = turn
    notify(thread, 'agent_state', { state: 'working', agentMode: thread.agentMode })
    for (;;) {
      const step = await options.answer(inferenceOf(thread))
      if (turn.cancelled)
        return
      if (!step) {
        failTurn(thread, 'The mock model has no scripted answer for this request.', 'leapmux_e2e_unscripted')
        return
      }
      if (step.delayMs && !await waitUnlessAborted(step.delayMs, turn.abort.signal))
        return
      if (step.error) {
        failTurn(thread, step.error.message, step.error.code ?? 'leapmux_e2e_error')
        return
      }
      const calls = (step.toolCalls ?? []).map(call => ({ call, id: ampToolUseID(thread.id, call.id) }))
      const content: ActorBlock[] = []
      if (step.reasoning !== undefined)
        content.push({ type: 'thinking', thinking: step.reasoning, signature: 'leapmux-e2e-signature', blockState: 'complete' })
      if (step.text !== undefined)
        content.push({ type: 'text', text: step.text, blockState: 'complete' })
      for (const { call, id } of calls)
        content.push({ type: 'tool_use', id, name: call.name, complete: true, input: call.arguments ?? {}, blockState: 'complete' })
      const messageId = ampMessageID()
      notify(thread, 'inference_tools', { messageId, agentMode: thread.agentMode, tools: [] })
      notify(thread, 'agent_state', { state: 'streaming', messageId, agentMode: thread.agentMode })
      addMessage(thread, { role: 'assistant', content, messageId, usage: usage(), state: { type: 'complete' } })

      if (calls.length === 0) {
        // A steering line that arrived after the last tool continues the turn.
        if (thread.steers.length > 0 && flushSteers(thread))
          continue
        thread.turn = undefined
        notify(thread, 'agent_state', { state: 'idle', messageId, agentMode: thread.agentMode })
        return
      }

      notify(thread, 'agent_state', { state: 'running_tools', messageId, agentMode: thread.agentMode })
      const runs = await Promise.all(calls.map(({ call, id }) => runTool(thread, turn, call, id, messageId)))
      if (turn.cancelled)
        return
      addMessage(thread, {
        role: 'user',
        content: calls.map(({ id }, index) => ({ type: 'tool_result', toolUseID: id, run: runs[index] })),
        messageId: ampMessageID(),
      })
      flushSteers(thread)
    }
  }

  /** Put every waiting steering line into the thread. Reports whether it put any. */
  function flushSteers(thread: ActorThread): boolean {
    const steers = thread.steers
    thread.steers = []
    for (const content of steers)
      addMessage(thread, { role: 'user', content, messageId: ampMessageID() })
    return steers.length > 0
  }

  /**
   * Run one tool call, and return the executor's `run` record.
   *
   * A subagent tool runs HERE, as Amp runs it on its server: its prompt is one more
   * inference, and the answer's text is its report. The mock leases every other tool to
   * the executor, which runs it -- after its permission rules, which is where the
   * LeapMux helper decides.
   */
  async function runTool(thread: ActorThread, turn: RunningTurn, call: MockModelToolCall, id: string, messageId: string): Promise<unknown> {
    const args = call.arguments ?? {}
    if (AMP_SERVER_SUBAGENT_TOOLS.has(call.name)) {
      const prompt = subagentPrompt(call.name, args)
      const step = await options.answer({
        body: { model: 'mock-model', system: '', messages: [{ role: 'user', content: [{ type: 'text', text: prompt }] }] },
        userText: prompt,
      })
      if (!step || step.error)
        return { status: 'error', error: { message: step?.error?.message ?? 'The mock model has no scripted answer for this subagent.' } }
      return { status: 'done', result: step.text ?? '' }
    }
    const executor = [...thread.sockets].find(member => member.executor)
    if (!executor)
      return { status: 'error', error: { message: 'No executor is connected to the thread.' } }
    const result = new Promise<unknown>((resolve) => {
      thread.leases.set(id, resolve)
      turn.abort.signal.addEventListener('abort', () => resolve({ status: 'cancelled', reason: 'User canceled' }), { once: true })
    })
    executor.connection.send(JSON.stringify({
      jsonrpc: '2.0',
      method: 'tool_lease',
      params: { toolCallId: id, toolName: EXECUTOR_TOOL_NAMES.get(call.name) ?? call.name, args, messageId },
    }))
    return result
  }

  return {
    handleHttp,
    handleUpgrade,
    close: () => {
      for (const thread of threads.values()) {
        if (thread.turn) {
          thread.turn.cancelled = true
          thread.turn.abort.abort()
        }
        for (const member of thread.sockets)
          member.connection.close()
      }
    },
  }
}

/** The request of one subagent call, which its tool states under a key of its own. */
function subagentPrompt(name: string, args: Record<string, unknown>): string {
  const pick = (key: string) => (typeof args[key] === 'string' ? args[key] as string : '')
  switch (name) {
    case AMP_SUBAGENT_TOOL.Task:
      return pick('prompt')
    case AMP_SUBAGENT_TOOL.Oracle:
      return [pick('task'), pick('context')].filter(Boolean).join('\n\n')
    default:
      return [pick('query'), pick('context')].filter(Boolean).join('\n\n')
  }
}

/**
 * The conversation so far, as one Anthropic Messages request body.
 *
 * The scenario machinery reads its marker and its matchers from a model request, and
 * this is the request the loop would send. A tool result carries its run record as
 * text, which is what a matcher on the result reads.
 */
export function ampInferenceOf(messages: readonly { role: string, content: readonly Record<string, unknown>[] }[]): AmpInference {
  const converted = messages.map(message => ({
    role: message.role,
    content: message.content.map((block) => {
      if (block.type === 'tool_result')
        return { type: 'tool_result', tool_use_id: block.toolUseID, content: JSON.stringify(block.run ?? null) }
      if (block.type === 'tool_use')
        return { type: 'tool_use', id: block.id, name: block.name, input: block.input }
      if (block.type === 'thinking')
        return { type: 'thinking', thinking: block.thinking }
      return block
    }),
  }))
  const lastUser = [...converted].reverse().find(message => message.role === 'user')
  return {
    body: { model: 'mock-model', system: '', messages: converted },
    userText: lastUser ? lastUser.content.map(blockText).join('\n') : '',
  }
}

function inferenceOf(thread: { messages: readonly ActorMessage[] }): AmpInference {
  return ampInferenceOf(thread.messages)
}

function blockText(block: Record<string, unknown>): string {
  if (typeof block.text === 'string')
    return block.text
  if (typeof block.content === 'string')
    return block.content
  return ''
}

function firstText(content: readonly Record<string, unknown>[]): string {
  return content.map(blockText).find(text => text.trim() !== '')?.trim() ?? ''
}

function messageCount(thread: ActorThread): number {
  return thread.messages.length + thread.seededMessageCount
}

function viewOf(thread: ActorThread): AmpThreadView {
  return { id: thread.id, agentMode: thread.agentMode, tree: thread.tree, title: thread.title, messageCount: messageCount(thread), archived: thread.archived }
}

function usage(): Record<string, unknown> {
  return {
    model: 'mock-model',
    maxInputTokens: 200_000,
    inputTokens: 10,
    outputTokens: 5,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    totalInputTokens: 10,
    timestamp: new Date().toISOString(),
    features: [],
  }
}

/** Wait for `delayMs`, or until the signal aborts. Reports whether the wait ran out. */
function waitUnlessAborted(delayMs: number, signal: AbortSignal): Promise<boolean> {
  if (signal.aborted)
    return Promise.resolve(false)
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve(true)
    }, delayMs)
    function onAbort(): void {
      clearTimeout(timer)
      resolve(false)
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

async function readJSON(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  for await (const chunk of request)
    chunks.push(Buffer.from(chunk))
  const text = Buffer.concat(chunks).toString('utf8')
  return text ? JSON.parse(text) : {}
}

function writeJSON(response: ServerResponse, status: number, value: unknown): void {
  response.writeHead(status, { 'content-type': 'application/json' }).end(JSON.stringify(value))
}
