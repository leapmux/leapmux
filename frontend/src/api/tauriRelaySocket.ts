/**
 * TauriRelayWebSocket provides a WebSocket-like interface that bridges through
 * Tauri IPC. Binary frames travel raw: the wrapper hands each relay open a
 * Tauri `Channel`, the Rust shell stores it beside the relay id, and frames
 * reach the handler as ArrayBuffers. Close notifications use the same channel
 * so a delayed frame still arrives before its relay closes.
 *
 * It satisfies ChannelManager's structural `ChannelSocket` surface (see
 * ~/lib/channel) so the channel layer drives a native browser WebSocket and
 * this relay wrapper through one code path without an erasing cast. That
 * contract cuts both ways: wherever a native socket isolates listeners,
 * orders events, or dedups registrations, this wrapper must too.
 */

import { platformBridge } from '~/api/platformBridge'
import { createLogger } from '~/lib/logger'
import { relayClaim } from '~/lib/relayClaim'

const log = createLogger('tauriRelaySocket')

type WSEventType = 'open' | 'close' | 'message' | 'error'
interface WSListener { handler: EventListener, once: boolean }

export class TauriRelayWebSocket {
  readyState: number = WebSocket.CONNECTING
  binaryType: BinaryType = 'arraybuffer'

  onopen: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null

  private listeners = new Map<WSEventType, WSListener[]>()
  private sendQueue: Promise<void> = Promise.resolve()
  // Frames sent before OPEN are buffered here and flushed once the relay opens,
  // matching native WebSocket semantics; without this a send that races
  // openChannelRelay() is dispatched to a not-yet-open relay and silently
  // dropped, which would lose a Noise handshake message.
  private pendingSends: (ArrayBuffer | Uint8Array)[] = []
  // Claims the singleton relay synchronously, at construction and so before any
  // await, so a predecessor's in-flight teardown can see it has been superseded
  // (see relayClaim).
  private readonly wrapperId = relayClaim.claim()
  // Whether this wrapper has already settled its relay claim (released the
  // installed relay or abandoned a claim whose open never landed). Close can race
  // the open chain -- close() schedules releaseRelay on the send chain's tail
  // while the open chain's post-open CLOSED-check calls it too -- and
  // releaseIfClaimable treats an unowned relay as claimable, so without this flag
  // the second call would fire closeChannelRelay again (idempotent at the sidecar
  // today, but a double-count hazard for any future relay-lifecycle logic).
  private relayReleased = false

  constructor() {
    void this.open()
  }

  private async open(): Promise<void> {
    let installed = false
    try {
      installed = await platformBridge.openChannelRelay(this.wrapperId, {
        isAbandoned: () => this.readyState === WebSocket.CLOSED,
        onMessage: (message) => {
          // The Rust slot clears asynchronously, so an in-flight message can
          // still reach the old wrapper after close().
          if (this.readyState === WebSocket.CLOSED)
            return
          if (message instanceof ArrayBuffer) {
            const ev = { data: message } as MessageEvent
            this.invokeHandler('onmessage', this.onmessage, ev)
            this.dispatch('message', ev)
          }
          else {
            this.handleClose(message as CloseEvent)
          }
        },
      })
      if (!installed) {
        this.abandonRelayClaim()
        return
      }
      if (this.readyState === WebSocket.CLOSED) {
        // Close raced the open. Release the relay if a successor has not
        // already taken the claim.
        this.releaseRelay()
        return
      }
      this.readyState = WebSocket.OPEN
      // Flush sends queued before OPEN ahead of any sends from an open handler.
      this.flushPendingSends()
      const ev = {} as Event
      this.invokeHandler('onopen', this.onopen, ev)
      this.dispatch('open', ev)
    }
    catch (err) {
      if (installed)
        this.releaseRelay()
      else
        this.abandonRelayClaim()
      if (!this.markClosed())
        return
      const ev = new ErrorEvent('error', {
        message: String(err),
      })
      this.invokeHandler('onerror', this.onerror, ev)
      this.dispatch('error', ev)
      // A native WebSocket fires close after a connection error.
      // ChannelManager drains its channels in the close handler.
      this.emitClose({
        code: 1006,
        reason: String(err),
        wasClean: false,
      } as CloseEvent)
    }
  }

  addEventListener(type: string, listener: EventListener, opts?: { once?: boolean }): void {
    const t = type as WSEventType
    let list = this.listeners.get(t)
    if (!list) {
      list = []
      this.listeners.set(t, list)
    }
    // Native EventTarget.addEventListener treats re-registering the same
    // (type, listener) pair as a no-op; without this a duplicate registration
    // would fire the handler twice per event -- and a once-listener registered
    // twice would survive its own first firing.
    if (list.some(l => l.handler === listener))
      return
    list.push({ handler: listener, once: opts?.once ?? false })
  }

  removeEventListener(type: string, listener: EventListener): void {
    const list = this.listeners.get(type as WSEventType)
    if (!list)
      return
    const idx = list.findIndex(l => l.handler === listener)
    if (idx >= 0)
      list.splice(idx, 1)
  }

  // invokeHandler isolates an `on*` property handler the way dispatch() isolates
  // addEventListener listeners: native EventTarget dispatch invokes every
  // listener -- the property handler included -- independently, so one throwing
  // consumer never suppresses the others. Without this, a throwing .onopen
  // would abort the open chain into its .catch, which reads the throw as an
  // open failure and reaps a perfectly healthy relay; a throwing .onclose would
  // skip dispatch('close') and with it ChannelManager's close-drain.
  private invokeHandler(name: string, handler: ((ev: never) => void) | null, ev: Event): void {
    if (!handler)
      return
    try {
      (handler as (ev: Event) => void)(ev)
    }
    catch (err) {
      log.error('channel relay handler threw', {
        handler: name,
        error: String(err),
      })
    }
  }

  private dispatch(type: WSEventType, ev: Event): void {
    const list = this.listeners.get(type)
    if (!list)
      return
    // Iterate a copy since once-listeners mutate the array. Isolate each handler:
    // dispatch runs from the raw frame-channel callback (not inside
    // a promise chain), so a throw in a consumer handler
    // would escape as an uncaught error, abort the remaining handlers of
    // this event type, and skip the once-cleanup for the throwing entry. This mirrors
    // the per-listener isolation channel.ts applies everywhere via safeCall.
    for (const entry of [...list]) {
      try {
        entry.handler(ev)
      }
      catch (err) {
        log.error('channel relay listener threw', {
          type,
          error: String(err),
        })
      }
      if (entry.once)
        this.removeEventListener(type, entry.handler)
    }
  }

  send(data: ArrayBuffer | Uint8Array): void {
    // Native WebSocket.send() throws InvalidStateError synchronously while still
    // CONNECTING and silently discards frames once CLOSING/CLOSED. This wrapper
    // instead buffers pre-OPEN and flushes on open so a send racing the relay open
    // is not silently dropped; after close it drops with a warning.
    if (this.readyState === WebSocket.CLOSED) {
      log.warn('channel relay send after close, dropping frame')
      return
    }
    // The frame passes through unchanged -- raw bytes over the Tauri IPC, no
    // base64 (see platformBridge.sendChannelMessage).
    if (this.readyState !== WebSocket.OPEN) {
      this.pendingSends.push(data)
      return
    }
    this.dispatchSend(data)
  }

  // dispatchSend serializes a single frame through the promise chain
  // so frames reach the Hub in order (Tauri command dispatch is async; without
  // serialization messages can overtake each other and break the Noise
  // protocol's sequential nonce counter).
  private dispatchSend(data: ArrayBuffer | Uint8Array): void {
    this.sendQueue = this.sendQueue.then(
      () => platformBridge.sendChannelMessage(data),
    ).catch((err) => {
      log.warn('channel relay send failed', {
        error: String(err),
      })
    })
  }

  private flushPendingSends(): void {
    const pending = this.pendingSends
    this.pendingSends = []
    for (const data of pending)
      this.dispatchSend(data)
  }

  // Mirrors WebSocket.close(code?, reason?): forward the caller's code/reason
  // into the emitted close event rather than hard-coding 1000/''. `this.ws` is
  // typed as a DOM WebSocket, so a no-arg close() silently dropped the arguments
  // a caller like ChannelManager passes (`close(1000, 'closed')`). A
  // caller-initiated close is clean, so wasClean stays true.
  close(code = 1000, reason = ''): void {
    if (this.readyState === WebSocket.CLOSED)
      return
    // Native WebSocket.close() transmits already-buffered data before closing.
    // Tearing the relay down synchronously instead let an already-dispatched
    // send land on a torn-down relay and fail into dispatchSend's catch, losing
    // the frame with no error surfaced -- the same silent-drop this wrapper's
    // pre-OPEN buffering exists to prevent. Defer the teardown behind the send
    // chain; releaseRelay re-checks ownership when it runs, so a successor that
    // claims the relay during the drain is still left alone.
    const queued = this.sendQueue
    const ev = { code, reason, wasClean: true } as CloseEvent
    this.markClosed()
    // Native WebSocket.close() fires the close event ASYNCHRONOUSLY. Emitting it
    // synchronously here re-entered close listeners (ChannelManager's
    // handleWebSocketClose) while the manager still held `this.ws === this`, so
    // the stale-socket guard the async native event fails against PASSED --
    // clearing the in-flight-open dedup cache mid-closeAll on desktop only.
    // Defer the emission so the caller's synchronous teardown completes first,
    // matching the native ordering. The state transition above stays
    // synchronous: a send() after close() must still drop.
    queueMicrotask(() => this.emitClose(ev))
    void queued.finally(() => this.releaseRelay())
  }

  // Only a wrapper that can release the claim sends a close. The sidecar
  // checks the id if a newer owner adopts the relay before the close arrives.
  private releaseRelay(): void {
    if (this.relayReleased)
      return
    if (!relayClaim.releaseIfClaimable(this.wrapperId))
      return
    this.relayReleased = true
    // If the close fails, the sidecar may keep its Hub WebSocket open.
    // A successor open can adopt or replace that relay.
    platformBridge.closeChannelRelay(this.wrapperId).catch((err) => {
      log.warn('channel relay close failed; leaving relay for successor adoption', {
        error: String(err),
      })
    })
  }

  // abandonRelayClaim drops this wrapper's claim when its open never landed, marking
  // the relay UNOWNED so a predecessor still driving a live one can reap it (see
  // relayClaim).
  private abandonRelayClaim(): void {
    if (this.relayReleased)
      return
    this.relayReleased = true
    relayClaim.abandon(this.wrapperId)
  }

  // The sidecar's channel callback delivers the close after earlier frames.
  // Release the claim and let Rust clear the delivery slot for this owner.
  private handleClose(ev: CloseEvent): void {
    if (!this.markClosed())
      return
    this.releaseRelay()
    this.emitClose(ev)
  }

  // Report whether this call transitioned the socket to CLOSED. Keep this
  // separate from emitClose so local close can defer the event.
  private markClosed(): boolean {
    if (this.readyState === WebSocket.CLOSED)
      return false
    this.readyState = WebSocket.CLOSED
    this.pendingSends = []
    return true
  }

  private emitClose(ev: CloseEvent): void {
    this.invokeHandler('onclose', this.onclose, ev)
    this.dispatch('close', ev)
  }
}
