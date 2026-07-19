/**
 * TauriRelayWebSocket provides a WebSocket-like interface that bridges through
 * Tauri IPC and events. Binary data is base64-encoded at the boundary.
 *
 * It satisfies ChannelManager's structural `ChannelSocket` surface (see
 * ~/lib/channel) so the channel layer drives a native browser WebSocket and
 * this relay wrapper through one code path without an erasing cast. That
 * contract cuts both ways: wherever a native socket isolates listeners,
 * orders events, or dedups registrations, this wrapper must too.
 */

import { parseRelayClosePayload, platformBridge } from '~/api/platformBridge'
import { arrayBufferToBase64, base64ToArrayBuffer } from '~/lib/base64'
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
  private pendingSends: string[] = []
  private unlistenMessage: (() => void) | null = null
  private unlistenClose: (() => void) | null = null
  // Claims the singleton relay synchronously, at construction and so before any
  // await, so a predecessor's in-flight teardown can see it has been superseded
  // (see relayClaim).
  private readonly wrapperId = relayClaim.claim()
  // Whether platformBridge.openChannelRelay() actually resolved, i.e. whether a relay
  // is installed that this wrapper is responsible for. A later step throwing in the
  // open chain does NOT mean the open failed.
  private relayInstalled = false
  // Whether this wrapper has already settled its relay claim (released the
  // installed relay or abandoned a claim whose open never landed). Close can race
  // the open chain -- close() schedules releaseRelay on the send chain's tail
  // while the open chain's post-open CLOSED-check calls it too -- and
  // releaseIfClaimable treats an unowned relay as claimable, so without this flag
  // the second call would fire closeChannelRelay again (idempotent at the sidecar
  // today, but a double-count hazard for any future relay-lifecycle logic).
  private relayReleased = false

  constructor() {
    Promise.all([
      platformBridge.onEvent('channel:message', (payload: unknown) => {
        // Decode inside a guard: this callback runs straight off the raw Tauri
        // event (no promise chain to catch for it), and atob throws on a
        // malformed payload -- an unguarded throw would escape uncaught AND
        // skip dispatch(), silently orphaning the frame with no log to find it
        // by. A frame that cannot be decoded is dropped loudly instead.
        let ev: MessageEvent
        try {
          ev = { data: base64ToArrayBuffer(payload as string) } as MessageEvent
        }
        catch (err) {
          log.error('channel relay message payload was undecodable; dropping frame', { error: String(err) })
          return
        }
        this.invokeHandler('onmessage', this.onmessage, ev)
        this.dispatch('message', ev)
      }),
      platformBridge.onEvent('channel:close', (payload: unknown) => {
        this.handleClose(parseRelayClosePayload(payload) as CloseEvent)
      }),
    ]).then(async ([unlistenMessage, unlistenClose]) => {
      this.unlistenMessage = unlistenMessage
      this.unlistenClose = unlistenClose
      if (this.readyState === WebSocket.CLOSED) {
        this.unlistenMessage()
        this.unlistenClose()
        this.unlistenMessage = null
        this.unlistenClose = null
        // This open never landed either (handleClose ran before openChannelRelay
        // was even attempted -- e.g. a predecessor relay's channel:close event
        // reached this wrapper's listener). Same rule as the catch below's
        // !relayInstalled arm: drop the claim, or it dangles on this dead wrapper
        // and a predecessor still driving a live relay can never reap it.
        this.abandonRelayClaim()
        return
      }
      await platformBridge.openChannelRelay(this.wrapperId)
      this.relayInstalled = true
      if (this.readyState === WebSocket.CLOSED) {
        // close() raced the open: the Go sidecar installed a live relay whose
        // owning wrapper is now gone (handleClose already unlistened). Tear it
        // down so it does not linger, emitting channel:message frames no
        // listener will consume until the next OpenChannelRelay reuses it --
        // unless a successor wrapper has already adopted it, in which case it is
        // the successor's to close.
        this.releaseRelay()
        return
      }
      this.readyState = WebSocket.OPEN
      // Flush any frames buffered before OPEN before firing the open event, so
      // they are queued ahead of any sends the open handler issues.
      this.flushPendingSends()
      const ev = {} as Event
      this.invokeHandler('onopen', this.onopen, ev)
      this.dispatch('open', ev)
    }).catch((err: unknown) => {
      // Two very different failures land here, and they need opposite handling.
      // Deciding on relayInstalled rather than on "the chain rejected" is the point:
      // a failure past the install (the flush machinery, a listener-teardown bug)
      // runs AFTER the relay is installed, and treating that as "the open never
      // landed" would abandon a live relay nobody then closes. (A throwing
      // CONSUMER handler never lands here at all: the on* invocations and
      // dispatch() isolate each handler, the way a native socket's event
      // dispatch does -- an application bug in an open listener must not tear
      // down a healthy relay.)
      if (this.relayInstalled)
        this.releaseRelay() // we own an installed relay; reap it
      else
        this.abandonRelayClaim() // never got one; let someone else own it
      if (this.readyState === WebSocket.CLOSED)
        return
      this.unlistenMessage?.()
      this.unlistenClose?.()
      this.unlistenMessage = null
      this.unlistenClose = null
      this.readyState = WebSocket.CLOSED
      const ev = new ErrorEvent('error', { message: String(err) })
      this.invokeHandler('onerror', this.onerror, ev)
      this.dispatch('error', ev)
      // A native WebSocket fires 'close' after 'error' on a failed connection, and
      // ChannelManager.ensureWebSocket drains channels in its close handler, so
      // emit one to keep the relay wrapper faithful to the surface it stands in
      // for -- and to reclaim state should a future caller have channels open
      // when a relay reopen fails. This catch already runs asynchronously (a
      // promise rejection), so emitting synchronously matches handleClose.
      this.emitClose({ code: 1006, reason: String(err), wasClean: false } as CloseEvent)
    })
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
      log.error('channel relay handler threw', { handler: name, error: String(err) })
    }
  }

  private dispatch(type: WSEventType, ev: Event): void {
    const list = this.listeners.get(type)
    if (!list)
      return
    // Iterate a copy since once-listeners mutate the array. Isolate each handler:
    // dispatch runs from the raw Tauri `channel:message` event callback (not inside
    // a promise chain), so a throw -- e.g. a malformed-frame decode in the message
    // handler -- would escape as an uncaught error, abort the remaining handlers of
    // this event type, and skip the once-cleanup for the throwing entry. This mirrors
    // the per-listener isolation channel.ts applies everywhere via safeCall.
    for (const entry of [...list]) {
      try {
        entry.handler(ev)
      }
      catch (err) {
        log.error('channel relay listener threw', { type, error: String(err) })
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
    const b64 = arrayBufferToBase64(data)
    if (this.readyState !== WebSocket.OPEN) {
      this.pendingSends.push(b64)
      return
    }
    this.dispatchSend(b64)
  }

  // dispatchSend serializes a single encoded frame through the promise chain
  // so frames reach the Hub in order (Tauri command dispatch is async; without
  // serialization messages can overtake each other and break the Noise
  // protocol's sequential nonce counter).
  private dispatchSend(b64: string): void {
    this.sendQueue = this.sendQueue.then(
      () => platformBridge.sendChannelMessage(b64),
    ).catch((err) => { log.warn('channel relay send failed', { error: String(err) }) })
  }

  private flushPendingSends(): void {
    const pending = this.pendingSends
    this.pendingSends = []
    for (const b64 of pending)
      this.dispatchSend(b64)
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

  // releaseRelay tears down the shared sidecar relay, but only while this wrapper may
  // claim it. A wrapper superseded by a successor must leave the relay alone:
  // CloseChannelRelay closes whichever relay is installed, so closing here would
  // kill the successor's (see relayClaim).
  private releaseRelay(): void {
    if (this.relayReleased)
      return
    if (!relayClaim.releaseIfClaimable(this.wrapperId))
      return
    this.relayReleased = true
    // Name the relay this wrapper owns. The claim check above orders us against other
    // WRAPPERS, but not against the sidecar: it runs each request on its own
    // goroutine with no ordering, so this close can execute after a successor's open
    // already adopted the relay. The id lets the sidecar ignore it in that case
    // instead of tearing down a relay someone else is using.
    //
    // A rejection here means the JS claim is already surrendered but the Go-side
    // relay may still be installed. That is self-healing -- the next
    // OpenChannelRelay adopts or supersedes whatever is installed -- but it must
    // not be SILENT: until that next open, the orphaned relay holds its Hub
    // WebSocket and emits frames nobody consumes.
    platformBridge.closeChannelRelay(this.wrapperId).catch((err) => {
      log.warn('channel relay close failed; leaving relay for successor adoption', { error: String(err) })
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

  // handleClose is the sidecar-initiated path (a channel:close event): that
  // callback already runs asynchronously relative to any caller, so state
  // transition and emission happen together, as a native close event would.
  // It settles the relay claim like every other terminal path -- the sidecar
  // just told us the relay it emitted for is gone, so the claim must not stay
  // pointed at this dead wrapper. releaseRelay re-checks ownership, so a
  // successor that already claimed is left alone, and its close RPC lets the
  // sidecar clear the dead relay's slot instead of waiting for the next open
  // to reap it.
  private handleClose(ev: CloseEvent): void {
    if (!this.markClosed())
      return
    this.releaseRelay()
    this.emitClose(ev)
  }

  // markClosed transitions to CLOSED and detaches the sidecar listeners,
  // reporting whether this call performed the transition. Split from emitClose
  // so close() can transition synchronously while deferring the event.
  private markClosed(): boolean {
    if (this.readyState === WebSocket.CLOSED)
      return false
    this.readyState = WebSocket.CLOSED
    this.pendingSends = []
    this.unlistenMessage?.()
    this.unlistenClose?.()
    this.unlistenMessage = null
    this.unlistenClose = null
    return true
  }

  private emitClose(ev: CloseEvent): void {
    this.invokeHandler('onclose', this.onclose, ev)
    this.dispatch('close', ev)
  }
}
