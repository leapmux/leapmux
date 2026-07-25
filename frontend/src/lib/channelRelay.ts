/**
 * Shared WebSocket relay for multiplexed E2EE channel traffic.
 *
 * Owns the single browser↔Hub socket (`ws` / `wsPromise`), open/close lifecycle,
 * length-prefixed frame decode, and the successor-dial close guard. Extracted
 * from ChannelManager so the transport state machine is unit-testable without
 * Noise / RPC harness. Channel routing and Hub-control parse stay with the
 * coordinator via injected callbacks.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelMessage } from '~/generated/leapmux/v1/channel_pb'
import { fromBinary } from '@bufbuild/protobuf'
import { ChannelMessageSchema } from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { unframeBytes } from './channelFraming'
import { createLogger } from './logger'

const log = createLogger('channel')

/** Reserved channel ID for Hub-originated control frames. */
export const HUB_CONTROL_CHANNEL_ID = '_hub'

/**
 * The narrow slice of the WebSocket surface ChannelRelay actually drives. Both a
 * native browser WebSocket and the Tauri IPC relay wrapper (TauriRelayWebSocket)
 * satisfy this structurally.
 */
export interface ChannelSocket {
  readyState: number
  // `send` takes exactly what ChannelManager writes -- an ArrayBuffer-backed
  // Uint8Array (or ArrayBuffer). Spelling the buffer generic keeps a native
  // WebSocket, whose send wants a BufferSource, assignable to this shape.
  send: (data: Uint8Array<ArrayBuffer> | ArrayBuffer) => void
  close: (code?: number, reason?: string) => void
  // `(ev: any)` keeps the listener loose enough that a native WebSocket's overloaded,
  // WebSocketEventMap-typed add/removeEventListener satisfy this interface.
  addEventListener: (type: string, listener: (ev: any) => void, opts?: { once?: boolean }) => void
  removeEventListener: (type: string, listener: (ev: any) => void) => void
}

export interface ChannelRelayDeps {
  createWebSocket: () => ChannelSocket
  wsOpenTimeoutMs: number
  /** Decrypted ChannelMessage for a worker channel (after length-prefix strip). */
  onFrame: (channelId: string, msg: ChannelMessage) => void
  /** Raw hub-control ChannelMessage (ciphertext still HubControlFrame bytes). */
  onHubControl: (msg: ChannelMessage) => void
  /**
   * Tear down every live channel after the current socket closes.
   * `successorDialing` is true when a newer ensureWebSocket owns wsPromise —
   * the coordinator must NOT clear the per-worker open dedup in that case.
   */
  onCloseDrain: (successorDialing: boolean) => void
}

/**
 * Owns the shared multiplexed WebSocket and its framing.
 */
export class ChannelRelay {
  private deps: ChannelRelayDeps
  private ws: ChannelSocket | null = null
  private wsPromise: Promise<void> | null = null
  /** CONNECTING socket for the in-flight dial; closed by closeWebSocket before onOpen. */
  private dialing: ChannelSocket | null = null
  /** Bumped by closeWebSocket / superseded dials so a stale onOpen cannot install this.ws. */
  private dialGeneration = 0

  constructor(deps: ChannelRelayDeps) {
    this.deps = deps
  }

  /** Whether the current socket is OPEN. */
  isOpen(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN
  }

  /**
   * Send a length-prefixed (or already-framed) buffer on the live socket.
   * Throws transport ChannelError when the socket is missing or not OPEN.
   */
  send(buf: Uint8Array): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      // Throw rather than log-and-return: call()/stream() wrap the send in a
      // try/catch that unregisters the just-registered pending request /
      // stream listener and rejects the caller. Swallowing this here would
      // leave that request live until its ~15s RPC timeout (or, for a stream,
      // forever) if the close event that would otherwise drain it is delayed or
      // superseded by a successor socket. A non-'client' source makes
      // onSendFailure tear the pooled channel down so the next call re-opens.
      // this.ws is only ever an OPEN-or-later socket (it is assigned in onOpen),
      // so a non-OPEN readyState here means CLOSING/CLOSED -- a dead transport.
      throw new ChannelError('transport', 'cannot send channel message: WebSocket not open')
    }
    // ChannelSocket.send wants ArrayBuffer-backed Uint8Array; our framed buffers
    // are always freshly allocated ArrayBuffers (never SharedArrayBuffer).
    this.ws.send(buf as Uint8Array<ArrayBuffer>)
  }

  /** Ensure the shared WebSocket is connected. */
  ensureWebSocket(): Promise<void> {
    // Already connected and open.
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      return Promise.resolve()
    }

    // Connection attempt already in progress - deduplicate.
    if (this.wsPromise) {
      return this.wsPromise
    }

    const dialGeneration = ++this.dialGeneration
    const ws = this.deps.createWebSocket()
    this.dialing = ws

    this.wsPromise = new Promise<void>((resolve, reject) => {
      // Mutual references between timer/onOpen/onError are unavoidable
      // for event handler setup; all are defined before any execute.
      /* eslint-disable ts/no-use-before-define */
      const timer = setTimeout(() => {
        ws.removeEventListener('open', onOpen)
        ws.removeEventListener('error', onError)
        ws.close()
        if (this.dialing === ws)
          this.dialing = null
        if (this.dialGeneration === dialGeneration) {
          this.ws = null
          this.wsPromise = null
        }
        reject(new ChannelError('transport', `WebSocket open timed out after ${Math.round(this.deps.wsOpenTimeoutMs / 1000)}s`))
      }, this.deps.wsOpenTimeoutMs)

      const onOpen = () => {
        clearTimeout(timer)
        ws.removeEventListener('error', onError)
        if (this.dialing === ws)
          this.dialing = null
        // closeWebSocket / a newer dial bumped dialGeneration — abandon this socket.
        if (this.dialGeneration !== dialGeneration) {
          ws.close(1000, 'superseded')
          reject(new ChannelError('transport', 'WebSocket dial superseded'))
          return
        }
        this.ws = ws
        this.wsPromise = null

        ws.addEventListener('message', (event: MessageEvent) => {
          // Same stale-socket fence as the close handler below: a superseded
          // socket can still deliver frames it buffered before it was replaced,
          // and routing them into the shared channel map would let a stale
          // CLOSE-flag frame drain a live channel -- or a stale data frame
          // advance a channel's Noise receive nonce -- on the successor's
          // watch.
          if (this.ws === ws) {
            this.handleWebSocketMessage(event)
          }
        })

        ws.addEventListener('close', () => {
          // Only the CURRENT socket's close tears the transport down. A stale
          // socket's close fires after readyState already flipped it out of the
          // OPEN fast path above, so a concurrent ensureWebSocket may have
          // opened and installed a successor as this.ws in the window -- acting
          // on the stale close here would drain the successor's channels, null
          // this.ws, and orphan the still-OPEN successor.
          if (this.ws === ws) {
            this.handleWebSocketClose()
          }
        })

        resolve()
      }

      const onError = (event: Event) => {
        clearTimeout(timer)
        ws.removeEventListener('open', onOpen)
        if (this.dialing === ws)
          this.dialing = null
        if (this.dialGeneration === dialGeneration) {
          this.ws = null
          this.wsPromise = null
        }
        const message = event instanceof ErrorEvent && event.message
          ? event.message
          : 'WebSocket connection failed'
        reject(new ChannelError('transport', message))
      }
      /* eslint-enable ts/no-use-before-define */

      ws.addEventListener('open', onOpen, { once: true })
      ws.addEventListener('error', onError, { once: true })
    })

    return this.wsPromise
  }

  closeWebSocket(): void {
    // Invalidate any in-flight CONNECTING dial before closing sockets so its
    // onOpen cannot reinstall this.ws after logout/closeAll (SCAN-1/2/3).
    this.dialGeneration++
    if (this.dialing) {
      try {
        this.dialing.close(1000, 'closed')
      }
      catch {
        // Best effort — some test doubles may not accept close args.
      }
      this.dialing = null
    }
    if (this.ws) {
      if (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING) {
        this.ws.close(1000, 'closed')
      }
      this.ws = null
    }
    this.wsPromise = null
  }

  /** Normalize WebSocket message data and route to the correct channel. */
  handleWebSocketMessage(event: MessageEvent): void {
    const raw = event.data
    let ab: ArrayBuffer
    if (raw instanceof ArrayBuffer) {
      ab = raw
    }
    else if (ArrayBuffer.isView(raw)) {
      // Node Buffer / SharedArrayBuffer-backed views need a copy; a full-cover
      // Uint8Array over a dedicated ArrayBuffer can pass through without slice.
      const view = raw as ArrayBufferView
      if (
        view.buffer instanceof ArrayBuffer
        && view.byteOffset === 0
        && view.byteLength === view.buffer.byteLength
      ) {
        ab = view.buffer
      }
      else {
        ab = view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength) as ArrayBuffer
      }
    }
    else {
      return
    }
    this.handleMultiplexedMessage(ab)
  }

  handleMultiplexedMessage(data: ArrayBuffer): void {
    const buf = new Uint8Array(data)
    // A framing violation is dropped, but never silently: a systematic
    // Hub<->browser framing desync would otherwise surface only as
    // unexplained RPC timeouts, unlike every other rejection in this file,
    // which logs.
    const framed = unframeBytes(buf)
    if (!framed.ok) {
      if (framed.failure.kind === 'short') {
        log.warn('dropping WebSocket frame shorter than its length prefix', { length: framed.failure.length })
      }
      else {
        log.warn('dropping WebSocket frame with a mismatched length prefix', {
          declared: framed.failure.declared,
          actual: framed.failure.actual,
        })
      }
      return
    }

    // Zero-copy view past the 4-byte length prefix. fromBinary decodes
    // synchronously and protobuf-es aliases the `ciphertext` bytes field as a
    // subarray of this input; both are safe because each inbound WS frame owns a
    // fresh, never-reused ArrayBuffer and ciphertext is consumed (decrypt /
    // hub-control parse) before the frame is dropped -- so no copy is needed.
    const msg: ChannelMessage = fromBinary(ChannelMessageSchema, framed.payload)

    if (msg.channelId === HUB_CONTROL_CHANNEL_ID) {
      this.deps.onHubControl(msg)
      return
    }

    this.deps.onFrame(msg.channelId, msg)
  }

  private handleWebSocketClose(): void {
    this.ws = null

    // A successor dial started in the gap between this socket's readyState leaving
    // OPEN and this queued close event firing owns this.wsPromise: the socket that
    // just closed nulled wsPromise in its own onOpen, so a non-null promise here can
    // only belong to a newer ensureWebSocket. Nulling it -- or clearing the
    // per-worker open dedup -- would orphan that successor's still-dialing socket (an
    // idle Hub connection once it opens) and let a duplicate channel-open start. This
    // completes the `this.ws === ws` guard at the close listener, which already skips
    // a close whose successor has fully OPENED but not one still DIALING.
    const successorDialing = this.wsPromise !== null

    this.deps.onCloseDrain(successorDialing)

    // Do NOT null wsPromise after drain when successorDialing was false: a sync
    // re-enter into ensureWebSocket from onCloseDrain (stream onError / state
    // listener) may have just claimed the slot — clearing it would orphan that
    // dial (WRAPPERS-R1). When a successor already owned the promise at close
    // time, leave it alone as before.
  }
}
