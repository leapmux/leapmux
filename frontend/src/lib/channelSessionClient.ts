import type {
  HandshakeBegunEvent,
  RequestDoneEvent,
  SessionCommand,
  SessionEvent,
} from './channelSessionProtocol'
import type { WorkerKeyBundle } from './workerKeyBundle'
import type { ChannelMessage, EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { assertNever } from './assertNever'
import { ownedBytes } from './bytes'
import { ChannelError } from './channelError'
import { channelErrorOf } from './channelSessionProtocol'
import { formatErrorMessage } from './errors'
import { createLogger, isDebugEnabled, onDebugEnabledChange } from './logger'

// ---------------------------------------------------------------------------
// Channel session client
//
// The main thread's side of the channel session worker (see
// channelSessionEndpoint.ts). It spawns the worker on first use, posts the
// commands, settles each request by its answer, and routes the other events to
// the channel manager.
//
// It is not built on createWorkerClient: the worker holds state (the keys of
// every channel), and one command can produce many events. createWorkerClient
// posts the pending requests of a crashed worker again to a fresh one, which
// would run them against a worker that has no keys. Here a lost worker fails
// every pending request, and the manager closes every channel (onWorkerLost).
// The next open spawns a fresh worker.
//
// This module must never import the endpoint, the Noise modules or the session
// modules: the browser main thread holds no cipher key
// (workerOnlyCode.test.ts).
// ---------------------------------------------------------------------------

const log = createLogger('channel')

/**
 * The longest one request may wait for its answer, in milliseconds. It covers the
 * load of the worker's module over a slow link, and a rekey round trip of up to
 * SESSION_VERIFY_TIMEOUT_MS (10 s). Past it, the worker counts as stuck: the
 * client terminates it and closes every channel. Without a budget, a module
 * fetch that stalls without an error would hold an open forever, and with it
 * every caller that waits on the same open.
 */
export const SESSION_REQUEST_BUDGET_MS = 30_000

/** What the client does with what a session worker posts. */
export interface SessionWorkerHandlers {
  message: (event: SessionEvent) => void
  /** The worker failed: it could not load, it threw outside a command, or a message could not be read. */
  failure: (message: string) => void
}

/**
 * The client's view of one session worker: a real Worker in a browser
 * (spawnChannelSessionWorker), or a MessageChannel pair in Node and the unit
 * tests (createInProcessSessionHost).
 */
export interface SessionWorkerPort {
  postMessage: (command: SessionCommand, transfer: Transferable[]) => void
  /** Start delivering the worker's events to `handlers`. Called once, right after the spawn. */
  connect: (handlers: SessionWorkerHandlers) => void
  terminate: () => void
}

/** Spawn the channel session worker of a browser page. */
export function spawnChannelSessionWorker(): SessionWorkerPort {
  const worker = new Worker(new URL('./channelSessionWorker.ts', import.meta.url), { type: 'module' })
  return {
    postMessage: (command, transfer) => worker.postMessage(command, transfer),
    connect: (handlers) => {
      worker.onmessage = (event: MessageEvent<SessionEvent>) => handlers.message(event.data)
      worker.onerror = (event: ErrorEvent) => handlers.failure(event.message || 'the worker failed to load')
      worker.onmessageerror = () => handlers.failure('a message from the worker could not be read')
    },
    terminate: () => worker.terminate(),
  }
}

/** A handshake that began in the session worker. */
export interface SessionHandshake {
  /** The id of the request that began it, which the worker files it under. */
  readonly id: number
  readonly message1: Uint8Array
}

/** What the session worker needs to finish a handshake and register the channel's session. */
export interface FinishHandshakeParams {
  channelId: string
  payload: Uint8Array
  maxReassembledMessageSize: number
}

export interface ChannelSessionClientDeps {
  /** Spawn a session worker. May throw. */
  spawn: () => SessionWorkerPort
  /** Put `frame` on the WebSocket. Called in the order in which the worker encrypted the frames. */
  onWire: (channelId: string, frame: Uint8Array) => void
  /** A complete, decrypted response or stream message. */
  onDeliver: (channelId: string, correlationId: number, plaintext: Uint8Array) => void
  /** An inbound chunked message broke a reassembly limit. */
  onReassemblyFailed: (channelId: string, correlationId: number, message: string) => void
  /** The worker ended a channel's session. */
  onSessionEnded: (channelId: string, error: ChannelError) => void
  /** The live worker crashed or got stuck. Called once for each lost worker. */
  onWorkerLost: (error: ChannelError) => void
  /** See SESSION_REQUEST_BUDGET_MS, the default. */
  requestBudgetMs?: number
}

/** The event that answers each kind of request. */
interface AnswerOf {
  handshakeBegun: HandshakeBegunEvent
  requestDone: RequestDoneEvent
}

type AnswerType = keyof AnswerOf

/** A request that waits for its answer. */
interface PendingSessionRequest {
  answer: AnswerType
  resolve: (event: AnswerOf[AnswerType]) => void
  reject: (err: ChannelError) => void
  timer: ReturnType<typeof setTimeout>
}

/** One spawned worker and the requests that wait on it. */
interface SpawnedSessionWorker {
  port: SessionWorkerPort
  pending: Map<number, PendingSessionRequest>
  /** Set once the client gave the worker up: it was lost or shut down. */
  retired: boolean
}

/** Terminate the worker behind `port`, and log a failure rather than throw it: the caller is already handling one. */
function terminateQuietly(port: SessionWorkerPort): void {
  try {
    port.terminate()
  }
  catch (err) {
    log.warn('terminating the channel session worker failed', {
      error: formatErrorMessage(err),
    })
  }
}

/** Whether `view` lies on a plain ArrayBuffer, which a transfer can move. */
function onArrayBuffer(view: Uint8Array): view is Uint8Array<ArrayBuffer> {
  return view.buffer instanceof ArrayBuffer
}

function stoppedError(): ChannelError {
  return new ChannelError('transport', 'channel session worker stopped')
}

function notRunningError(): ChannelError {
  return new ChannelError('transport', 'channel session worker is not running')
}

export class ChannelSessionClient {
  private readonly deps: ChannelSessionClientDeps
  private readonly requestBudgetMs: number
  private live: SpawnedSessionWorker | null = null
  /** One counter for every worker that this client spawns, so that no id repeats. */
  private nextRequestId = 1
  /** The worker that holds each handshake that began, by handshake id. */
  private readonly handshakeOwners = new Map<number, SpawnedSessionWorker>()

  constructor(deps: ChannelSessionClientDeps) {
    this.deps = deps
    this.requestBudgetMs = deps.requestBudgetMs ?? SESSION_REQUEST_BUDGET_MS
    // The worker has a logger of its own, and every frame-level debug line
    // happens there. The client lives as long as the page, so it never
    // unsubscribes.
    onDebugEnabledChange((enabled) => {
      if (this.live !== null)
        this.post(this.live, { type: 'configure', debug: enabled }, [])
    })
  }

  /**
   * Spawn the worker now, ahead of the first handshake, so that it loads while
   * the open waits for the Hub. A spawn that throws is left for the next request,
   * which reports it.
   */
  prepare(): void {
    try {
      this.liveWorker()
    }
    catch {
      // beginHandshake spawns again, and rejects with the spawn failure.
    }
  }

  /** Build handshake message 1 in the session worker. */
  async beginHandshake(mode: EncryptionMode, keys: WorkerKeyBundle): Promise<SessionHandshake> {
    const worker = this.liveWorker()
    const requestId = this.nextRequestId++
    const begun = await this.request(worker, {
      type: 'beginHandshake',
      requestId,
      mode,
      keys,
    }, 'handshakeBegun')
    this.handshakeOwners.set(requestId, worker)
    return { id: requestId, message1: begun.message1 }
  }

  /**
   * Finish `handshake` with the Worker's message 2, and register the channel's
   * session in the worker. The handshake is spent either way. Rejects when the
   * worker that began the handshake is gone, because no other worker holds its
   * state.
   */
  async finishHandshake(handshake: SessionHandshake, params: FinishHandshakeParams): Promise<void> {
    const owner = this.handshakeOwners.get(handshake.id)
    this.handshakeOwners.delete(handshake.id)
    if (owner === undefined || owner !== this.live)
      throw stoppedError()
    const requestId = this.nextRequestId++
    await this.request(owner, {
      type: 'finishHandshake',
      requestId,
      handshakeId: handshake.id,
      channelId: params.channelId,
      payload: params.payload,
      maxReassembledMessageSize: params.maxReassembledMessageSize,
    }, 'requestDone')
  }

  /** Zero the state of a handshake that will not finish. */
  abandonHandshake(handshake: SessionHandshake): void {
    const owner = this.handshakeOwners.get(handshake.id)
    this.handshakeOwners.delete(handshake.id)
    if (owner !== undefined && owner === this.live)
      this.post(owner, { type: 'abandonHandshake', handshakeId: handshake.id }, [])
  }

  /**
   * Hand one message to the worker, which encrypts it and posts its frames
   * through `onWire`. The worker takes the bytes: a full-cover `plaintext` is
   * transferred and detached here, and any other view is copied first. Throws
   * when no worker runs, or when the message cannot be posted.
   */
  send(channelId: string, correlationId: number, plaintext: Uint8Array, startsRequest: boolean): void {
    const worker = this.live
    if (worker === null)
      throw notRunningError()
    const owned = ownedBytes(plaintext)
    worker.port.postMessage({
      type: 'send',
      channelId,
      correlationId,
      plaintext: owned,
      startsRequest,
    }, [owned.buffer])
  }

  /** The request or the stream of `correlationId` ended. */
  release(channelId: string, correlationId: number): void {
    if (this.live !== null)
      this.post(this.live, { type: 'release', channelId, correlationId }, [])
  }

  /** Rekey every session whose rekey is due, as after a wake from sleep. */
  checkRekey(): void {
    if (this.live !== null)
      this.post(this.live, { type: 'checkRekey' }, [])
  }

  /** Zero and drop the session of a channel that the main thread closed. */
  close(channelId: string): void {
    if (this.live !== null)
      this.post(this.live, { type: 'close', channelId }, [])
  }

  /**
   * Hand one inbound frame to the worker. The relay gives every inbound frame a
   * fresh ArrayBuffer of its own, which nothing reads after this, so the whole
   * frame buffer is transferred, and the worker reads the ciphertext as a view
   * into it.
   */
  forwardInbound(channelId: string, msg: ChannelMessage): void {
    const worker = this.live
    if (worker === null)
      return
    const ciphertext = onArrayBuffer(msg.ciphertext)
      ? msg.ciphertext
      : ownedBytes(msg.ciphertext)
    this.post(worker, {
      type: 'inbound',
      channelId,
      correlationId: msg.correlationId,
      flags: msg.flags,
      ciphertext,
    }, [ciphertext.buffer])
  }

  /**
   * Make the channel's session ready to serve a request (see
   * ChannelSession.ensureReady). Rejects when the worker ended the session; the
   * worker posts `sessionEnded` first, so the channel is already closed then.
   */
  async ensureReady(channelId: string): Promise<void> {
    const worker = this.live
    if (worker === null)
      throw notRunningError()
    const requestId = this.nextRequestId++
    await this.request(worker, { type: 'ensureReady', requestId, channelId }, 'requestDone')
  }

  /**
   * Stop the worker: it zeroes every session and handshake, and closes itself.
   * The client detaches at once and fails every pending request, and the next
   * open spawns a fresh worker.
   */
  shutdown(): void {
    const worker = this.live
    if (worker === null)
      return
    this.live = null
    worker.retired = true
    try {
      worker.port.postMessage({ type: 'shutdown' }, [])
    }
    catch (err) {
      log.warn('the channel session worker did not take the shutdown; terminating it', {
        error: formatErrorMessage(err),
      })
      terminateQuietly(worker.port)
    }
    this.rejectPending(worker, stoppedError())
  }

  /** The live worker, spawned now when there is none. Throws when it cannot spawn. */
  private liveWorker(): SpawnedSessionWorker {
    if (this.live !== null)
      return this.live
    let port: SessionWorkerPort
    try {
      port = this.deps.spawn()
    }
    catch (err) {
      throw new ChannelError('transport', `channel session worker could not start: ${formatErrorMessage(err)}`)
    }
    const worker: SpawnedSessionWorker = {
      port,
      pending: new Map(),
      retired: false,
    }
    try {
      port.connect({
        message: event => this.handleEvent(worker, event),
        failure: message => this.lose(worker, new ChannelError('transport', `channel session worker failed: ${message}`)),
      })
      port.postMessage({
        type: 'configure',
        debug: isDebugEnabled(),
      }, [])
    }
    catch (err) {
      // The worker never served a channel, so nothing else needs to know.
      worker.retired = true
      terminateQuietly(port)
      throw new ChannelError('transport', `channel session worker could not start: ${formatErrorMessage(err)}`)
    }
    this.live = worker
    return worker
  }

  /** Post `command`. A post that throws loses the worker, because it no longer sees every command. */
  private post(worker: SpawnedSessionWorker, command: SessionCommand, transfer: Transferable[]): void {
    try {
      worker.port.postMessage(command, transfer)
    }
    catch (err) {
      this.lose(worker, new ChannelError('transport', `channel session worker failed: ${formatErrorMessage(err)}`))
    }
  }

  /** Post a request, and settle with its answer, or reject with its failure. */
  private request<K extends AnswerType>(worker: SpawnedSessionWorker, command: SessionCommand & { requestId: number }, answer: K): Promise<AnswerOf[K]> {
    return new Promise<AnswerOf[K]>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.lose(worker, new ChannelError('transport', 'channel session worker stopped answering'))
      }, this.requestBudgetMs)
      worker.pending.set(command.requestId, {
        answer,
        resolve: resolve as (event: AnswerOf[AnswerType]) => void,
        reject,
        timer,
      })
      try {
        worker.port.postMessage(command, [])
      }
      catch (err) {
        // The message never reached the worker, so it fails alone.
        clearTimeout(timer)
        worker.pending.delete(command.requestId)
        reject(new ChannelError('transport', `post to the channel session worker: ${formatErrorMessage(err)}`))
      }
    })
  }

  /** Take the pending request `requestId` of `worker` out, with its timer stopped. */
  private takePending(worker: SpawnedSessionWorker, requestId: number): PendingSessionRequest | undefined {
    const request = worker.pending.get(requestId)
    if (request === undefined) {
      log.warn('the channel session worker answered a request that does not wait', { request_id: requestId })
      return undefined
    }
    worker.pending.delete(requestId)
    clearTimeout(request.timer)
    return request
  }

  private settle(worker: SpawnedSessionWorker, event: AnswerOf[AnswerType]): void {
    const request = this.takePending(worker, event.requestId)
    if (request === undefined)
      return
    if (event.type === request.answer)
      request.resolve(event)
    else
      request.reject(new ChannelError('transport', `the channel session worker answered request ${event.requestId} with ${event.type}, not ${request.answer}`))
  }

  private handleEvent(worker: SpawnedSessionWorker, event: SessionEvent): void {
    // A worker that the client gave up may still post what it queued.
    if (worker !== this.live)
      return
    try {
      switch (event.type) {
        case 'handshakeBegun':
        case 'requestDone':
          this.settle(worker, event)
          return
        case 'requestFailed':
          this.takePending(worker, event.requestId)?.reject(channelErrorOf(event.error))
          return
        case 'wire':
          this.deps.onWire(event.channelId, event.frame)
          return
        case 'deliver':
          this.deps.onDeliver(event.channelId, event.correlationId, event.plaintext)
          return
        case 'reassemblyFailed':
          this.deps.onReassemblyFailed(event.channelId, event.correlationId, event.message)
          return
        case 'sessionEnded':
          this.deps.onSessionEnded(event.channelId, channelErrorOf(event.error))
          return
        default:
          assertNever(event)
      }
    }
    catch (err) {
      // A throw here would escape into the message dispatch of the port.
      log.error('handling an event of the channel session worker failed', { event: event.type, error: err })
    }
  }

  /** Fail every pending request of `worker` with `error`. */
  private rejectPending(worker: SpawnedSessionWorker, error: ChannelError): void {
    const pending = [
      ...worker.pending.values(),
    ]
    worker.pending.clear()
    for (const request of pending) {
      clearTimeout(request.timer)
      request.reject(error)
    }
  }

  /**
   * `worker` crashed or got stuck: terminate it, and fail its pending requests.
   * When it was the live worker, detach from it, and tell the manager, which
   * closes every channel. Acts once for each worker.
   */
  private lose(worker: SpawnedSessionWorker, error: ChannelError): void {
    if (worker.retired)
      return
    worker.retired = true
    log.error('lost the channel session worker', { error: error.message })
    terminateQuietly(worker.port)
    this.rejectPending(worker, error)
    if (this.live !== worker)
      return
    this.live = null
    this.deps.onWorkerLost(error)
  }
}
