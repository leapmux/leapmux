import type { ChannelErrorSource } from './channelError'
import type { Reassembler } from './reassembler'
/**
 * Per-channel RPC / stream multiplexer for the E2EE channel.
 *
 * Owns correlation-id → pending unary / stream-listener registration, response
 * delivery, reassembly-limit rejection, and waiter drain. Extracted from
 * ChannelManager so the request state machine is unit-testable without a
 * WebSocket / Noise harness. ChannelManager keeps rekey gating on `call` /
 * `stream`, then delegates here.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { InnerRpcResponse, InnerStreamMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { InnerMessageSchema, InnerRpcRequestSchema, InnerStreamRequestSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { abortError, ChannelError } from './channelError'
import { formatErrorMessage } from './errors'
import { createLogger } from './logger'

const log = createLogger('channel')

export interface PendingRequest {
  resolve: (resp: InnerRpcResponse) => void
  reject: (err: Error) => void
}

export interface StreamListener {
  onMessage: (msg: InnerStreamMessage) => void
  onEnd: () => void
  onError: (err: Error) => void
}

/**
 * The slice of an ActiveChannel the RPC mux drives: correlation registries,
 * reassembly buffers, and the id allocator.
 */
export interface RpcChannel {
  channelId: string
  workerId: string
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  reassembly: Reassembler
  nextRequestId: number
}

export interface StreamHandle {
  requestId: number
  onMessage: (cb: (msg: InnerStreamMessage) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
  /**
   * Register the stream listener and encrypt+send. ChannelManager calls this
   * on the fast path synchronously, or after ensureRekeyed on the deferred path.
   * Returns a ChannelError when send fails (caller delivers via onError / throw).
   */
  attachAndSend: () => ChannelError | null
  /**
   * Deliver an error that happened before attach (rekey failure, channel closed
   * mid-gate). Uses onError when set; otherwise logs — matching the prior
   * ChannelManager.stream deferred-path behavior.
   */
  deliverDeferredError: (err: Error, logWhat: string) => void
}

export interface ChannelRpcDeps {
  /** Encrypt+send one outbound frame. Throws on failure (session/transport/size). */
  send: (ch: RpcChannel, plaintext: Uint8Array, requestId: number) => void
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
export function buildStreamRequestPlaintext(payload: Uint8Array, cancel: boolean): Uint8Array {
  const streamReq = create(InnerStreamRequestSchema, {
    payload,
    cancel,
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

  /**
   * Register a unary RPC, install timeout/abort cleanup, and encrypt+send.
   * Caller must have already passed rekey gating.
   */
  callAfterRekey(
    ch: RpcChannel,
    channelId: string,
    method: string,
    payload: Uint8Array,
    timeoutMs?: number,
    signal?: AbortSignal,
  ): Promise<InnerRpcResponse> {
    const requestId = ch.nextRequestId++
    const effectiveTimeoutMs = timeoutMs ?? this.deps.rpcTimeoutFn()

    return new Promise<InnerRpcResponse>((resolve, reject) => {
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
        reject(new ChannelError('client', `RPC call '${method}' timed out after ${timeoutSec}s (channel=${channelId})`))
      }, effectiveTimeoutMs)
      /* eslint-enable ts/no-use-before-define */

      log.debug('sending inner RPC request', { channel_id: ch.channelId, id: requestId, method, payload_len: payload.length })

      ch.pendingRequests.set(requestId, {
        resolve: (resp) => {
          cleanup()
          resolve(resp)
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
   * Allocate a stream correlation id and return a handle. The caller (after any
   * rekey gate) must invoke `attachAndSend` to register the listener and put the
   * request on the wire.
   */
  beginStream(ch: RpcChannel, method: string, payload: Uint8Array): StreamHandle {
    const requestId = ch.nextRequestId++
    let messageCb: ((msg: InnerStreamMessage) => void) | null = null
    let endCb: (() => void) | null = null
    let errorCb: ((err: Error) => void) | null = null

    log.debug('sending inner RPC request', { channel_id: ch.channelId, id: requestId, method, payload_len: payload.length })

    const plaintext = buildRequestPlaintext(method, payload)
    const attachAndSend = (): ChannelError | null => {
      ch.streamListeners.set(requestId, {
        onMessage: msg => messageCb?.(msg),
        onEnd: () => endCb?.(),
        onError: err => errorCb?.(err),
      })
      return this.trySend(ch, plaintext, requestId)
    }

    return {
      requestId,
      onMessage: (cb) => { messageCb = cb },
      onEnd: (cb) => { endCb = cb },
      onError: (cb) => { errorCb = cb },
      attachAndSend,
      deliverDeferredError: (err, logWhat) => {
        if (errorCb)
          errorCb(err)
        else
          log.warn(logWhat, { channel_id: ch.channelId, error: err.message })
      },
    }
  }

  /** Encrypt+send one outbound frame; unregister + onSendFailure on throw. */
  trySend(ch: RpcChannel, plaintext: Uint8Array, requestId: number): ChannelError | null {
    try {
      this.deps.send(ch, plaintext, requestId)
      return null
    }
    catch (err) {
      this.unregisterRequest(ch, requestId)
      this.deps.onSendFailure(ch, err)
      // Non-ChannelError throws are session/runtime failures (encrypt, etc.),
      // not client validation — report as transport so callers reconnect.
      return err instanceof ChannelError ? err : new ChannelError('transport', formatErrorMessage(err))
    }
  }

  /**
   * Send a follow-up (or cancel) frame on an open stream. Reuses the stream's
   * OWN correlation id — never ch.nextRequestId++ — because the frame is
   * addressed to the subscription, not to a new call.
   *
   * On cancel the local registration is dropped immediately; the worker's
   * terminal end frame then lands on an unregistered id and is discarded.
   */
  sendOnStream(ch: RpcChannel, correlationId: number, payload: Uint8Array, cancel: boolean): ChannelError | null {
    if (!ch.streamListeners.has(correlationId)) {
      // Distinguish "already gone" from success so callers do not advance
      // local interest after a silent no-op.
      return new ChannelError('client', 'stream not registered')
    }
    const plaintext = buildStreamRequestPlaintext(payload, cancel)
    if (cancel) {
      // Drop the local registration first so a racing end frame is discarded.
      this.unregisterRequest(ch, correlationId)
    }
    try {
      this.deps.send(ch, plaintext, correlationId)
      return null
    }
    catch (err) {
      this.deps.onSendFailure(ch, err)
      return err instanceof ChannelError ? err : new ChannelError('transport', formatErrorMessage(err))
    }
  }

  /**
   * Fail every waiter on a channel and release its buffers.
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

    ch.reassembly.clear()
  }

  /** Route a completed InnerRpcResponse to its pending request. */
  deliverResponse(ch: RpcChannel, correlationId: number, resp: InnerRpcResponse): void {
    if (log.isDebug()) {
      log.debug('received inner RPC response', {
        channel_id: ch.channelId,
        correlation_id: correlationId,
        is_error: resp.isError,
        error_code: resp.errorCode,
        error_message: resp.errorMessage,
        payload_len: resp.payload.length,
      })
    }
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      this.unregisterRequest(ch, correlationId)
      if (resp.isError) {
        const err = new ChannelError('rpc', resp.errorMessage || `RPC error code ${resp.errorCode}`, { code: resp.errorCode })
        this.deps.notifyError(ch.workerId, err)
        pending.reject(err)
      }
      else {
        pending.resolve(resp)
      }
      return
    }

    // A unary reply on a correlation id we registered as a STREAM. Without
    // this arm the frame is dropped and the subscription waits forever
    // with no error to retry from. Errors only: a non-error unary payload on
    // a stream id is not something a listener can interpret.
    if (!resp.isError) {
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (!listener) {
      return
    }
    this.unregisterRequest(ch, correlationId)
    const err = new ChannelError('rpc', resp.errorMessage || `RPC error code ${resp.errorCode}`, { code: resp.errorCode })
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
        is_error: streamMsg.isError,
        error_code: streamMsg.errorCode,
        error_message: streamMsg.errorMessage,
        payload_len: streamMsg.payload.length,
      })
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      // Unregister BEFORE invoking the terminal callback, and isolate every
      // listener call with safeCall, mirroring drainChannel.
      if (streamMsg.isError) {
        const err = new ChannelError('stream', streamMsg.errorMessage || `stream error code ${streamMsg.errorCode}`, { code: streamMsg.errorCode })
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
   * Drop every handler registered for a request and, with them, its reassembly buffer.
   *
   * A correlation id is either a pending unary or a stream, never both, so clearing
   * both maps is safe and keeps the rule -- a reassembly buffer lives and dies with the
   * request that owns it -- in ONE place.
   */
  unregisterRequest(ch: RpcChannel, correlationId: number): void {
    ch.pendingRequests.delete(correlationId)
    ch.streamListeners.delete(correlationId)
    ch.reassembly.drop(correlationId)
  }

  /**
   * Report a reassembly-limit breach to the request that owns the id, then tombstone
   * the id so the rest of its chunks are dropped in silence.
   */
  failReassembly(ch: RpcChannel, correlationId: number, message: string): void {
    this.rejectPendingRequest(ch, correlationId, 'client', message)
    ch.reassembly.poison(correlationId)
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
      // Isolate the app callback: failReassembly poisons the id AFTER this returns,
      // and a throwing onError would skip the poison.
      this.deps.safeCall(() => listener.onError(new ChannelError(source, message)), 'stream onError listener')
    }
  }

  /** Whether any handler is registered for this correlation id (unary, stream, or rekey). */
  hasHandler(ch: RpcChannel, correlationId: number, rekeyRequestId: number | null): boolean {
    return ch.pendingRequests.has(correlationId)
      || ch.streamListeners.has(correlationId)
      || correlationId === rekeyRequestId
  }
}
