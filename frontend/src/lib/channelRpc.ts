import type { ChannelErrorSource } from './channelError'
/**
 * Per-channel RPC / stream multiplexer for the E2EE channel.
 *
 * Owns correlation-id → pending unary / stream-listener registration, response
 * delivery, reassembly-limit rejection, and waiter drain. Extracted from
 * ChannelManager so the request state machine is unit-testable without a
 * WebSocket / Noise harness. The channel session worker encrypts what this
 * sends, and holds a send back while a rekey is due (see ChannelSession.send).
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { InnerRpcResponse, InnerStreamMessage, RpcError } from '~/generated/proto/leapmux/v1/channel_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { InnerMessageSchema, InnerRpcRequestSchema, InnerStreamRequestSchema, StreamCancelSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { abortError, ChannelError } from './channelError'
import { formatErrorMessage } from './errors'
import { createLogger } from './logger'

const log = createLogger('channel')

/** The ChannelError for the in-band error of an inner RPC or stream. */
function inBandChannelError(source: 'rpc' | 'stream', error: RpcError): ChannelError {
  const fallback = source === 'rpc'
    ? `RPC error code ${error.code}`
    : `stream error code ${error.code}`
  return new ChannelError(source, error.message || fallback, { code: error.code })
}

/**
 * The outcome of a unary InnerRpcResponse: the serialized response message, or
 * the in-band error that failed the call. A response that selects neither
 * result is a success whose response message is empty, so its payload is
 * empty.
 */
type UnaryOutcome = { error: RpcError } | { error: undefined, payload: Uint8Array }

function unaryOutcome(resp: InnerRpcResponse): UnaryOutcome {
  switch (resp.result.case) {
    case 'error':
      return { error: resp.result.value }
    case 'payload':
      return { error: undefined, payload: resp.result.value }
    case undefined:
      return {
        error: undefined,
        payload: new Uint8Array(),
      }
  }
}

/**
 * The action of a client frame on an open stream (InnerStreamRequest): a
 * revision that carries the method-specific payload, or the cancel that
 * retires the stream. An empty payload is a revision too.
 */
export type StreamAction = { case: 'payload', payload: Uint8Array } | { case: 'cancel' }

export interface PendingRequest {
  /** Receives the serialized response message of a successful call. */
  resolve: (payload: Uint8Array) => void
  reject: (err: Error) => void
}

export interface StreamListener {
  onMessage: (msg: InnerStreamMessage) => void
  onEnd: () => void
  onError: (err: Error) => void
}

/**
 * The slice of an ActiveChannel the RPC mux drives: correlation registries and
 * the id allocator.
 */
export interface RpcChannel {
  channelId: string
  workerId: string
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  nextRequestId: number
}

export interface StreamHandle {
  requestId: number
  onMessage: (cb: (msg: InnerStreamMessage) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
  /**
   * End the stream for its consumer with `err`, through its onError listener,
   * or a log line when it has none. No listener fires after this one, so a
   * frame that is still in flight cannot follow the failure.
   */
  fail: (err: Error, logWhat: string) => void
}

export interface ChannelRpcDeps {
  /**
   * Hand one outbound message to the channel session, which encrypts it and puts
   * it on the wire. `startsRequest` is true for the message that opens a request
   * or a stream. It throws for a message over the size limit, and when the session
   * worker does not run. A failure on the wire arrives later, through the
   * manager's `onSendFailure`, which closes the channel and so fails the request.
   */
  send: (ch: RpcChannel, plaintext: Uint8Array, requestId: number, startsRequest: boolean) => void
  /** The request or the stream of `correlationId` ended: the session drops what it kept for it. */
  release: (ch: RpcChannel, correlationId: number) => void
  /**
   * Decide the channel's fate after send threw. Called after the mux has already
   * unregistered the request id.
   */
  onSendFailure: (ch: RpcChannel, err: unknown) => void
  rpcTimeoutFn: () => number
  notifyError: (workerId: string, error: ChannelError) => void
  /**
   * Isolate a user-supplied listener callback so one throw cannot skip mux
   * bookkeeping or unwind through WebSocket dispatch.
   */
  safeCall: (fn: () => void, description: string) => void
}

/**
 * Encode an InnerRpcRequest into its InnerMessage envelope plaintext — the one
 * wire-encoding step `call` and `stream` share, so the request framing can only
 * be defined in one place.
 */
export function buildRequestPlaintext(method: string, payload: Uint8Array): Uint8Array {
  const innerReq = create(InnerRpcRequestSchema, {
    method,
    payload,
  })
  const envelope = create(InnerMessageSchema, {
    kind: { case: 'request', value: innerReq },
  })
  return toBinary(InnerMessageSchema, envelope)
}

/**
 * Encode an InnerStreamRequest envelope — the client→worker frame on an
 * ALREADY-OPEN stream. Sibling of buildRequestPlaintext, so the two request
 * framings live side by side.
 */
export function buildStreamRequestPlaintext(action: StreamAction): Uint8Array {
  const streamReq = create(InnerStreamRequestSchema, {
    action: action.case === 'payload'
      ? { case: 'payload', value: action.payload }
      : {
          case: 'cancel',
          value: create(StreamCancelSchema),
        },
  })
  const envelope = create(InnerMessageSchema, {
    kind: { case: 'streamRequest', value: streamReq },
  })
  return toBinary(InnerMessageSchema, envelope)
}

/**
 * Owns per-channel pending-request / stream-listener maps and the delivery /
 * drain paths that settle them.
 */
export class ChannelRpcMux {
  private deps: ChannelRpcDeps

  constructor(deps: ChannelRpcDeps) {
    this.deps = deps
  }

  /** Register a unary RPC, install timeout/abort cleanup, and send. */
  call(
    ch: RpcChannel,
    method: string,
    payload: Uint8Array,
    timeoutMs?: number,
    signal?: AbortSignal,
  ): Promise<Uint8Array> {
    const requestId = ch.nextRequestId++
    const effectiveTimeoutMs = timeoutMs ?? this.deps.rpcTimeoutFn()

    return new Promise<Uint8Array>((resolve, reject) => {
      const timeoutSec = Math.round(effectiveTimeoutMs / 1000)
      // timer + cleanup + abortListener form a mutually-referencing
      // teardown trio; lint-disable so the natural setup order can
      // stay together instead of being split by the no-use-before-define
      // rule.
      /* eslint-disable ts/no-use-before-define */
      let abortListener: (() => void) | undefined
      const cleanup = () => {
        clearTimeout(timer)
        if (abortListener && signal)
          signal.removeEventListener('abort', abortListener)
      }
      const timer = setTimeout(() => {
        this.unregisterRequest(ch, requestId)
        cleanup()
        log.debug('inner RPC request timed out', { channel_id: ch.channelId, id: requestId, method })
        reject(new ChannelError('client', `RPC call '${method}' timed out after ${timeoutSec}s (channel=${ch.channelId})`))
      }, effectiveTimeoutMs)
      /* eslint-enable ts/no-use-before-define */

      log.debug('sending inner RPC request', {
        channel_id: ch.channelId,
        id: requestId,
        method,
        payload_len: payload.length,
      })

      ch.pendingRequests.set(requestId, {
        resolve: (responsePayload) => {
          cleanup()
          resolve(responsePayload)
        },
        reject: (err) => {
          cleanup()
          reject(err)
        },
      })

      if (signal) {
        abortListener = () => {
          // Drop the pending entry so the eventual InnerRpcResponse
          // is treated as orphan + ignored (and with it any partial
          // reassembly, which existed only to feed this request).
          // cleanup() also clears the timer + this listener so no
          // double-resolve fires.
          this.unregisterRequest(ch, requestId)
          cleanup()
          log.debug('inner RPC request aborted by caller', { channel_id: ch.channelId, id: requestId, method })
          reject(abortError(signal, method))
        }
        signal.addEventListener('abort', abortListener, { once: true })
      }

      const plaintext = buildRequestPlaintext(method, payload)
      // The registration and the timer are already installed, and a throw out of a
      // Promise executor rejects the promise WITHOUT unwinding them -- the entry would
      // linger until the timeout fired and the timer would burn for its full duration
      // on a request that never reached the wire. Undo both here, then let
      // onSendFailure decide whether the channel itself is still usable.
      const sendErr = this.trySend(ch, plaintext, requestId)
      if (sendErr) {
        cleanup()
        reject(sendErr)
      }
    })
  }

  /**
   * Allocate a stream correlation id, register its listener, and send the
   * opening request. The registration comes first, so a reply that arrives in
   * the same turn finds its listener. Throws the send failure, after it
   * unregistered the listener.
   */
  beginStream(ch: RpcChannel, method: string, payload: Uint8Array): StreamHandle {
    const requestId = ch.nextRequestId++
    let messageCb: ((msg: InnerStreamMessage) => void) | null = null
    let endCb: (() => void) | null = null
    let errorCb: ((err: Error) => void) | null = null

    log.debug('sending inner RPC request', {
      channel_id: ch.channelId,
      id: requestId,
      method,
      payload_len: payload.length,
    })

    ch.streamListeners.set(requestId, {
      onMessage: msg => messageCb?.(msg),
      onEnd: () => endCb?.(),
      onError: err => errorCb?.(err),
    })
    const sendErr = this.trySend(ch, buildRequestPlaintext(method, payload), requestId)
    if (sendErr)
      throw sendErr

    return {
      requestId,
      onMessage: (cb) => { messageCb = cb },
      onEnd: (cb) => { endCb = cb },
      onError: (cb) => { errorCb = cb },
      fail: (err, logWhat) => {
        // Clear the listeners first, so a listener that throws cannot leave
        // them in place.
        const listener = errorCb
        messageCb = null
        endCb = null
        errorCb = null
        if (listener)
          listener(err)
        else
          log.warn(logWhat, { channel_id: ch.channelId, error: err.message })
      },
    }
  }

  /** Send the message that opens a request; unregister + onSendFailure on throw. */
  trySend(ch: RpcChannel, plaintext: Uint8Array, requestId: number): ChannelError | null {
    try {
      this.deps.send(ch, plaintext, requestId, true)
      return null
    }
    catch (err) {
      this.unregisterRequest(ch, requestId)
      this.deps.onSendFailure(ch, err)
      // Non-ChannelError throws are session/runtime failures (encrypt, etc.),
      // not client validation — report as transport so callers reconnect.
      return err instanceof ChannelError
        ? err
        : new ChannelError('transport', formatErrorMessage(err))
    }
  }

  /**
   * Send a follow-up (or cancel) frame on an open stream. Reuses the stream's
   * OWN correlation id — never ch.nextRequestId++ — because the frame is
   * addressed to the subscription, not to a new call.
   *
   * On cancel the local registration is dropped immediately; the worker's
   * final end frame then lands on an unregistered id and is discarded.
   */
  sendOnStream(ch: RpcChannel, correlationId: number, action: StreamAction): ChannelError | null {
    if (!ch.streamListeners.has(correlationId)) {
      // Distinguish "already gone" from success so callers do not advance
      // local interest after a silent no-op.
      return new ChannelError('client', 'stream not registered')
    }
    const plaintext = buildStreamRequestPlaintext(action)
    if (action.case === 'cancel') {
      // Drop the local registration first so a racing end frame is discarded.
      // The release reaches the session before the cancel, so a cancel whose
      // stream never left the session's held lane never leaves it either.
      this.unregisterRequest(ch, correlationId)
    }
    try {
      // Never a starting send: that would make a cancelled id live again.
      this.deps.send(ch, plaintext, correlationId, false)
      return null
    }
    catch (err) {
      this.deps.onSendFailure(ch, err)
      return err instanceof ChannelError
        ? err
        : new ChannelError('transport', formatErrorMessage(err))
    }
  }

  /**
   * Fail every waiter on a channel.
   *
   * `streamTermination` differs by caller: a local close is an orderly end its
   * consumer asked for, while a transport failure is an error the consumer must see.
   * Callers own removing the channel from the pool; this only settles what is
   * registered on it.
   */
  drainChannel(ch: RpcChannel, err: ChannelError, streamTermination: 'end' | 'error'): void {
    for (const [, pending] of ch.pendingRequests) {
      pending.reject(err)
    }
    ch.pendingRequests.clear()

    for (const [, listener] of ch.streamListeners) {
      if (streamTermination === 'end')
        this.deps.safeCall(() => listener.onEnd(), 'stream onEnd listener')
      else
        this.deps.safeCall(() => listener.onError(err), 'stream onError listener')
    }
    ch.streamListeners.clear()
  }

  /** Route a completed InnerRpcResponse to its pending request. */
  deliverResponse(ch: RpcChannel, correlationId: number, resp: InnerRpcResponse): void {
    const outcome = unaryOutcome(resp)
    if (log.isDebug()) {
      log.debug('received inner RPC response', {
        channel_id: ch.channelId,
        correlation_id: correlationId,
        is_error: outcome.error !== undefined,
        error_code: outcome.error?.code,
        error_message: outcome.error?.message,
        payload_len: outcome.error === undefined
          ? outcome.payload.length
          : 0,
      })
    }
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      this.unregisterRequest(ch, correlationId)
      if (outcome.error !== undefined) {
        const err = inBandChannelError('rpc', outcome.error)
        this.deps.notifyError(ch.workerId, err)
        pending.reject(err)
      }
      else {
        pending.resolve(outcome.payload)
      }
      return
    }

    // A unary reply on a correlation id we registered as a STREAM. Without
    // this arm the frame is dropped and the subscription waits forever
    // with no error to retry from. Errors only: a non-error unary payload on
    // a stream id is not something a listener can interpret.
    if (outcome.error === undefined) {
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (!listener) {
      return
    }
    this.unregisterRequest(ch, correlationId)
    const err = inBandChannelError('rpc', outcome.error)
    this.deps.notifyError(ch.workerId, err)
    this.deps.safeCall(() => listener.onError(err), 'stream onError listener')
  }

  /** Route an InnerStreamMessage to its stream listener. */
  deliverStream(ch: RpcChannel, correlationId: number, streamMsg: InnerStreamMessage): void {
    if (log.isDebug()) {
      log.debug('received inner stream message', {
        channel_id: ch.channelId,
        correlation_id: correlationId,
        end: streamMsg.end,
        is_error: streamMsg.error !== undefined,
        error_code: streamMsg.error?.code,
        error_message: streamMsg.error?.message,
        payload_len: streamMsg.payload.length,
      })
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      // Unregister BEFORE invoking the terminal callback, and isolate every
      // listener call with safeCall, mirroring drainChannel.
      if (streamMsg.error) {
        const err = inBandChannelError('stream', streamMsg.error)
        this.deps.notifyError(ch.workerId, err)
        this.unregisterRequest(ch, correlationId)
        this.deps.safeCall(() => listener.onError(err), 'stream onError listener')
      }
      else if (streamMsg.end) {
        this.unregisterRequest(ch, correlationId)
        this.deps.safeCall(() => listener.onEnd(), 'stream onEnd listener')
      }
      else {
        this.deps.safeCall(() => listener.onMessage(streamMsg), 'stream onMessage listener')
      }
    }
  }

  /**
   * Drop every handler registered for a request, and tell the session, which then
   * drops its reassembly buffer.
   *
   * A correlation id is either a pending unary or a stream, never both, so clearing
   * both maps is safe and keeps the rule -- a reassembly buffer lives and dies with the
   * request that owns it -- in ONE place.
   */
  unregisterRequest(ch: RpcChannel, correlationId: number): void {
    ch.pendingRequests.delete(correlationId)
    ch.streamListeners.delete(correlationId)
    this.deps.release(ch, correlationId)
  }

  /** Reject a pending request or error an active stream. */
  rejectPendingRequest(ch: RpcChannel, correlationId: number, source: ChannelErrorSource, message: string): void {
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      this.unregisterRequest(ch, correlationId)
      pending.reject(new ChannelError(source, message))
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      this.unregisterRequest(ch, correlationId)
      // Isolate the app callback, so that a throwing onError cannot unwind
      // through the event dispatch of the channel session worker.
      this.deps.safeCall(() => listener.onError(new ChannelError(source, message)), 'stream onError listener')
    }
  }
}
