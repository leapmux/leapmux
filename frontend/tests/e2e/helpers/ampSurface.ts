/**
 * Serve Amp's native remote endpoints and actor loop.
 *
 * The command line interface (CLI) calls REST endpoints and a JSON-RPC WebSocket for each thread actor.
 * The remote actor controls the loop and leases local tools to the CLI executor.
 * This module selects each inference through the shared model script.
 * Native probes confirmed the following protocol requirements.
 *
 * - The actor sends an unsolicited pong text frame when a socket opens.
 *   The CLI requires that first frame before it sends a request.
 * - Message IDs use M- plus exactly 22 base62 characters.
 *   Tool-use IDs use TU- plus exactly 22 base62 characters.
 *   The CLI silently rejects other IDs, and the turn never ends.
 * - The CLI opens a thread-client socket and an executor socket.
 *   The service sends thread notifications to both sockets.
 *   Only the socket that sends executor_connect receives tool leases.
 * - The executor identifier for shell_command is async_shell_command.
 *   Other local tools retain their model identifiers.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Duplex } from 'node:stream'
import type { MockModelScriptHost, SelectedModelAnswer } from './mockModelRequest'
import type { MockModelCredential, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { WebSocketConnection } from './webSocketServer'
import { Buffer } from 'node:buffer'
import { createHash, randomBytes, randomUUID } from 'node:crypto'
// RELATIVE imports, not `~/...`. See the note in `../agentSettings.ts`.
import { AMP_TOOL_NAME } from '../../../src/components/chat/providers/amp/toolNames'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '../../../src/generated/contracts/amp-protocol'
import { mockCredentialReceipt } from './mockCredentials'
import { bufferModelOutput, createBufferedModelStream } from './modelStream'
import { pauseUntilAborted } from './responsePause'
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
  mockCredential?: MockModelCredential
}

export interface AmpSurfaceOptions {
  /**
   * Answer one inference, or return undefined when no script supplies an answer.
   * The native service then sends an error that ends the CLI process with its reason.
   */
  answer: (inference: AmpInference, signal: AbortSignal) => Promise<MockModelStep | undefined>
  errorSent?: (inference: AmpInference, error: { code: string, message: string }) => void
  holdGeneration?: (inference: AmpInference, step: MockModelStep, signal: AbortSignal) => Promise<boolean>
}

/** Project Amp actor inference into the shared script without exposing its accounting state. */
export function ampScriptOptions(host: MockModelScriptHost): AmpSurfaceOptions {
  const requests = new WeakMap<AmpInference, Extract<SelectedModelAnswer, { kind: 'step' }>>()
  return {
    answer: async (inference, signal) => {
      const answer = host.select({
        protocol: 'anthropic-messages',
        path: AMP_ACTOR_PATH_PREFIX,
        body: inference.body,
        systemText: '',
        userText: inference.userText,
        ...(inference.mockCredential ? { mockCredential: inference.mockCredential } : {}),
      })
      if (answer.kind !== 'step')
        return undefined
      requests.set(inference, answer)
      if (answer.step.gate && !await answer.holdGate(answer.step.gate, { signal }))
        return undefined
      return answer.step
    },
    errorSent: (inference, error) => requests.get(inference)?.recordServiceError({ kind: 'amp-error-set', ...error }),
    holdGeneration: async (inference, _step, signal) => {
      const answer = requests.get(inference)
      if (!answer)
        throw new Error('The native Amp generation has no selected scenario.')
      return answer.bufferGeneration(signal)
    },
  }
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
  messages: Array<{ role: 'user' | 'assistant', text: string }>
  archived?: boolean
  /** Milliseconds since the epoch. Defaults to now. */
  updatedAt?: number
}

function isAmpSeededThread(value: unknown): value is AmpSeededThread {
  if (
    !isRecord(value)
    || typeof value.id !== 'string'
    || value.id === ''
    || typeof value.title !== 'string'
    || typeof value.tree !== 'string'
  ) {
    return false
  }
  if (value.archived !== undefined && typeof value.archived !== 'boolean')
    return false
  if (value.updatedAt !== undefined && (typeof value.updatedAt !== 'number' || !Number.isFinite(value.updatedAt)))
    return false
  return Array.isArray(value.messages) && value.messages.every(message =>
    isRecord(message) && (message.role === 'user' || message.role === 'assistant') && typeof message.text === 'string')
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
  sockets: Set<ThreadSocket>
  turn: RunningTurn | undefined
  /** Steering messages that arrived during the turn, for its next interruption point. */
  steers: ActorBlock[][]
  /** The executor's answer to each lease that waits for one. */
  leases: Map<string, (run: unknown) => void>
  mockCredential?: MockModelCredential
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
        const seeded = await readJSON(request)
        if (!isAmpSeededThread(seeded)) {
          writeJSON(response, 400, { error: 'The seeded Amp thread is invalid.' })
          return
        }
        const thread = threadFor(seeded.id)
        thread.title = seeded.title
        thread.tree = seeded.tree
        thread.messages = seeded.messages.map(message => ({
          role: message.role,
          content: [{ type: 'text', text: message.text, blockState: 'complete' }],
          messageId: ampMessageID(),
        }))
        thread.seq = thread.messages.length
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
      const credential = mockCredentialReceipt(request.headers)
      thread.mockCredential = { kind: 'service', accepted: credential.accepted }
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
      case 'mcpListAccountServers':
        return { ok: true, result: { servers: [] } }
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
    const credential = mockCredentialReceipt(request.headers)
    if (credential.kind !== 'none')
      thread.mockCredential = { kind: 'service', accepted: credential.accepted }
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
   * Send the native error that ends the CLI process.
   *
   * A failed or cancelled turn discards its steering lines.
   * A retained line would otherwise start an unscripted inference in the next turn.
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
    try {
      await runTurnSteps(thread, turn)
    }
    catch (error) {
      if (turn.cancelled || thread.turn !== turn)
        return
      const message = error instanceof Error ? error.message : String(error)
      failTurn(thread, message, 'leapmux_e2e_mock_failure')
    }
  }

  async function waitForGeneration(inference: AmpInference, step: MockModelStep, turn: RunningTurn): Promise<boolean> {
    if (turn.cancelled)
      return false
    if (step.stream) {
      const completed = options.holdGeneration
        ? await options.holdGeneration(inference, step, turn.abort.signal)
        : await bufferModelOutput(createBufferedModelStream(turn.abort.signal, step.stream), step)
      if (!completed || turn.cancelled)
        return false
    }
    if (step.delayMs && !await pauseUntilAborted(step.delayMs, turn.abort.signal))
      return false
    return !turn.cancelled
  }

  async function runTurnSteps(thread: ActorThread, turn: RunningTurn): Promise<void> {
    for (;;) {
      const inference = inferenceOf(thread)
      const step = await options.answer(inference, turn.abort.signal)
      if (turn.cancelled)
        return
      if (!step) {
        failTurn(thread, 'The mock model has no scripted answer for this request.', 'leapmux_e2e_unscripted')
        return
      }
      if (!await waitForGeneration(inference, step, turn))
        return
      if (step.error) {
        const code = step.error.code ?? 'leapmux_e2e_error'
        failTurn(thread, step.error.message, code)
        options.errorSent?.(inference, { code, message: step.error.message })
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
      addMessage(thread, { role: 'assistant', content, messageId, usage: usage(step), state: { type: 'complete' } })

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
      const inference: AmpInference = {
        body: { model: 'mock-model', system: '', messages: [{ role: 'user', content: [{ type: 'text', text: prompt }] }] },
        userText: prompt,
        ...(thread.mockCredential ? { mockCredential: thread.mockCredential } : {}),
      }
      const step = await options.answer(inference, turn.abort.signal)
      if (!step)
        return { status: 'error', error: { message: 'The mock model has no scripted answer for this subagent.' } }
      if (!await waitForGeneration(inference, step, turn))
        return { status: 'cancelled', reason: 'User canceled' }
      if (step.error)
        return { status: 'error', error: { message: step.error.message } }
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

function inferenceOf(thread: { messages: readonly ActorMessage[], mockCredential?: MockModelCredential }): AmpInference {
  return { ...ampInferenceOf(thread.messages), ...(thread.mockCredential ? { mockCredential: thread.mockCredential } : {}) }
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
  return thread.messages.length
}

function viewOf(thread: ActorThread): AmpThreadView {
  return { id: thread.id, agentMode: thread.agentMode, tree: thread.tree, title: thread.title, messageCount: messageCount(thread), archived: thread.archived }
}

function usage(step: MockModelStep): Record<string, unknown> {
  const inputTokens = step.usage?.inputTokens ?? 10
  const outputTokens = step.usage?.outputTokens ?? 5
  return {
    model: 'mock-model',
    maxInputTokens: step.usage?.contextWindow ?? 200_000,
    inputTokens,
    outputTokens,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    totalInputTokens: inputTokens,
    timestamp: new Date().toISOString(),
    features: [],
  }
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
