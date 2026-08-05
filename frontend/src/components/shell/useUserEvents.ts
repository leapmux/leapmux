import type { HLC, UserMaterialized } from '~/generated/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import type { HLCClock, PendingOpsManager } from '~/lib/crdt'
import type { ActiveClientStore } from '~/lib/presence/activeClient'
import { fromBinary } from '@bufbuild/protobuf'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { isTauriApp, parseRelayClosePayload, platformBridge } from '~/api/platformBridge'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { KEY_USER_EVENTS_RELAY_SEQ } from '~/lib/browserStorage'
import { unframeBytes } from '~/lib/channelFraming'
import { formatHlcWire, parseHlcWire } from '~/lib/crdt/hlc'
import { createLogger } from '~/lib/logger'
import { createPersistedSeq } from '~/lib/persistedSeq'
import { createExponentialBackoff } from '~/lib/retry'

const log = createLogger('useUserEvents')

/**
 * Whether a hub payload naming `frameUserID` may be adopted by a session whose
 * own id is `ownUserID`.
 *
 * FAIL-CLOSED and defined ONCE. Both adoption points -- the `initial`
 * UserMaterialized and the `delta` ResumeDelta -- must refuse a payload that
 * names another tenant, and `loadHydrationState` applies the same rule to the
 * persisted checkpoint. Two hand-copied predicates meant a change to this
 * security-relevant rule was one edit away from covering only one frame kind;
 * they had already drifted to logging through different channels.
 *
 * An unknown local id refuses rather than adopts. A payload that names NO
 * tenant is allowed through: proto3 normalizes an unset string to "", and the
 * per-socket generation guard already answers "is this frame stale?" -- this
 * one only answers "is this frame mine?".
 */
function isOwnTenant(ownUserID: string, frameUserID: string, what: string): boolean {
  if (!ownUserID || (frameUserID && frameUserID !== ownUserID)) {
    log.warn(`refusing a ${what} for another tenant`, { expected: ownUserID, got: frameUserID })
    return false
  }
  return true
}

const RECONNECT_BASE_DELAY_MS = 250
const RECONNECT_MAX_DELAY_MS = 5_000
// Single-key backoff: the hook drives one connection at a time, so one key is
// enough to escalate the reconnect delay across attempts and reset it on success.
const RECONNECT_KEY = 'userevents'

// Close codes on which auto-reconnect is futile: a genuine authorization or
// protocol failure where retrying in a loop cannot succeed. Every OTHER close --
// clean (1000/1001), transient (1012/1013), or an abnormal transport drop
// (1006, no close frame) -- is a reconnect signal, so a network blip never
// kills the subscription. This is intentionally broader than the backend's
// channelwire.isRecoverableCloseCode (which drives the CLI's clean-exit, not a
// long-lived subscription's reconnect): here only a hard terminal close stops
// the retry loop and is surfaced to the caller.
const TERMINAL_CLOSE_CODES = new Set<number>([
  1002, // protocol error
  1008, // policy violation -- the hub's /ws/userevents "forbidden" / auth expiry
])

function isTerminalCloseCode(code: number): boolean {
  return TERMINAL_CLOSE_CODES.has(code)
}

/**
 * Round-trip a resume watermark through the wire codec and return the PARSED
 * value, or undefined when it is absent or unusable.
 *
 * Two things this settles that a bare truthiness check did not. First, the
 * value that goes on the wire is now the one the validator approved: the caller
 * used to evaluate `parseHlcWire(formatHlcWire(watermark))` purely for its
 * truthiness, discard it, and send the unvalidated sibling. They are equal
 * today (the round trip is idempotent for a well-formed HLC), but only by
 * assumption, and the parsed value has the further benefit of being a fresh
 * message rather than a live reference into the manager's state.
 *
 * Second, it actually degrades. formatHlcWire THROWS a TypeError on a
 * non-string clientId by design -- so a non-proto caller fails loudly -- and
 * calling it bare from inside the WS-open effect meant that throw escaped the
 * reactive computation AFTER the teardown, killing the subscription for the
 * rest of the page session. That is the opposite of the "degrade to a
 * full-snapshot reconnect" the call site promised.
 */
function validateResumeHlc(watermark: HLC | undefined): HLC | undefined {
  if (!watermark)
    return undefined
  try {
    return parseHlcWire(formatHlcWire(watermark)) ?? undefined
  }
  catch {
    // Malformed beyond what the wire format can express: no cursor, so the
    // connect takes a full snapshot.
    return undefined
  }
}

// Ids for the desktop sidecar's userevents relay, handed out in dispatch order: the
// sidecar compares them to ignore a close whose relay a later open already replaced,
// and to ignore an open a later one has superseded. A stale-looking open matters
// here because the hub only sends UserMaterialized at subscribe time -- a dropped
// open means user events silently never bootstrap. The persisted monotonic counter
// (shared with the channel relay's claim ids -- see createPersistedSeq for the
// reload rationale) keeps a fresh page's ids above whatever the still-live sidecar
// already holds.
/** Exported for tests; production code reaches it only through useUserEvents. */
export const nextUserEventsRelayId = createPersistedSeq(KEY_USER_EVENTS_RELAY_SEQ)

/**
 * The resume cursor this client presents on reconnect: its highest applied
 * canonical HLC plus the epoch it was seen under. Sent on the /ws/userevents
 * URL so the hub can ship a ResumeDelta instead of a full snapshot. Resolved
 * from the per-user persisted watermark at socket-open.
 */
export interface ResumeToken {
  hlc: HLC
  epoch: bigint
}

/**
 * useUserEvents opens a single per-user WebSocket connection at
 * `/ws/userevents?workspace_ids=...` and dispatches every
 * incoming `WatchUserEvent` frame into the local PendingOpsManager /
 * ActiveClientStore.
 *
 * **WebSocket transport rationale**: HTTP/1.1 chunked streaming over
 * intermediaries (corporate proxies, the desktop sidecar's Tauri
 * proxy) is unreliable — buffers can hold the body until upstream
 * close, which would freeze a long-lived event stream. WebSocket
 * negotiates Upgrade and bypasses those buffers. The wire format is
 * `[4-byte big-endian length][protobuf-encoded WatchUserEvent]` per
 * binary frame, mirroring the `/ws/channel` E2EE relay so a single
 * read helper handles both endpoints.
 *
 * The hook stays alive for the lifetime of the authenticated session —
 * when the user switches workspaces, the same connection keeps running
 * and the layered projection is sliced per-workspace from the
 * materialized state. Reconnects re-bootstrap; clients dedup echoed
 * batches via `batch_id`.
 */
export interface UseUserEventsOpts {
  /** Reactive accessor for the user id. Empty disables the connection. */
  userId: () => string
  /**
   * Reactive accessor for workspace_ids the caller may read; empty array = all.
   *
   * NOTHING IN THE APP PASSES THIS, and wiring it needs care. The hub documents
   * a constraint on `SubscribeWithACL`: a cursor minted under a NARROW filter
   * and replayed under a WIDER one can miss ops, because the persisted cursor is
   * per-user, not per-filter. It has no producer today precisely because every
   * browser subscription resolves to all owned workspaces.
   *
   * Since the checkpoint seed landed, cursors are also CROSS-TAB -- a new tab
   * adopts a sibling's -- so a per-tab workspace filter would let two tabs
   * disagree about the filter a cursor was minted under, which is exactly the
   * pairing that constraint warns about.
   *
   * A non-empty value here therefore suppresses the resume cursor, so a
   * narrowed subscription always takes a full snapshot. That closes the URL
   * half, and only the URL half -- it is NOT the whole invariant, and a producer
   * cannot be wired on the strength of it alone.
   *
   * The half still open is the CHECKPOINT. A narrowed tab's confirmed state
   * holds only its own workspaces, but the watermark it persists beside them is
   * the hub's GLOBAL max_hlc (materializedFromState stamps `state.max_hlc` on
   * every snapshot, filtered or not). A sibling with no filter can adopt that
   * pair and RESUME on it -- validateCheckpoint checks the tenant and the
   * ranges, and has no filter to check against -- and the hub then ships only
   * what is strictly after a cursor that already sits above every entity the
   * narrowed writer filtered out. Those entities never arrive.
   *
   * So wiring a producer needs one of: the mint-time filter carried alongside
   * the cursor and re-checked by the hub (what SubscribeWithACL's doc calls for),
   * or a narrowed tab excluded from writing an adoptable checkpoint at all.
   * Until then this option has no producer on purpose.
   */
  allowedWorkspaceIds?: () => string[]
  /** Active-client store fed by PresenceUpdate events. */
  activeClient: ActiveClientStore
  /** PendingOpsManager that owns confirmed + speculative state. */
  pending: () => PendingOpsManager | null
  /**
   * Gate accessor for the WS open effect: the effect will not open the socket
   * until this returns true. useCrdtRuntime sets it once hydration (loading +
   * replaying the persisted checkpoint + op-log) has completed, so the resume
   * cursor resolves against hydrated confirmedState rather than empty state.
   * Undefined/always-true preserves the pre-hydration behavior (open as soon
   * as userId is truthy).
   */
  ready?: () => boolean
  /** Optional override for the WebSocket URL builder (tests). */
  buildWsUrl?: (workspaceIds: string[], resume: ResumeToken | null) => string
  /** Called when an EntityRemoved drops a pending op (caller may toast). */
  onPendingDropped?: () => void
  /**
   * Called when workspace lifecycle changes arrive. The callback typically
   * re-fetches the user's workspace list so the sidebar reflects creates,
   * renames, and deletes. Routed through a single hook so the workspace
   * store, section store, and registry each get their refresh in one place.
   */
  onWorkspaceLifecycleChanged?: () => void
  /**
   * Called whenever the hub tells us our effective subscriber identity
   * via `UserMaterialized.subscriber_client_id`. The hub derives this
   * from the authenticated session (or bearer token id) — the active-
   * client gate compares broadcast `active_client_id` against this,
   * NOT against the local random nanoid, because the hub stamps
   * `PresenceUpdate.active_client_id` from the same session identity
   * and the two would never otherwise match. Fired once per bootstrap.
   */
  onSubscriberClientId?: (clientId: string) => void
  /**
   * Called when the userevents stream closes with a terminal code (an
   * authorization/protocol failure, e.g. auth expiry) where auto-reconnect is
   * futile. The hook stops retrying and hands the caller the close code/reason
   * so it can surface a toast/banner (e.g. prompt a reload or re-auth) instead
   * of looping. Recoverable/transient closes reconnect silently and never fire
   * this.
   */
  onFatalClose?: (info: { code: number, reason: string }) => void
}

export interface UserEventsHook {
  /** True once the initial UserMaterialized has been received. */
  bootstrapped: () => boolean
  /** HLC clock the local pending manager uses. */
  clock: () => HLCClock | null
  /**
   * Force a teardown + fresh subscribe. Used by `useOpsSubmitter` when
   * the hub rejects a SubmitOps batch as `epoch_required` or
   * `stale_epoch` — the client must refresh `currentEpoch` via a new
   * `UserMaterialized` before retrying. Returns a promise that resolves
   * once the WS has been closed; the next bootstrap event arrives
   * asynchronously on the new connection.
   */
  reconnect: () => Promise<void>
}

export function useUserEvents(opts: UseUserEventsOpts): UserEventsHook {
  const [bootstrapped, setBootstrapped] = createSignal(false)
  const [clock, setClock] = createSignal<HLCClock | null>(null)
  // reconnectKey forces the userId effect to re-fire even when userId
  // hasn't changed — used by `reconnect()` to drop the current WS
  // and start a fresh subscription. The new UserMaterialized refreshes
  // `pending.currentEpoch` so the next SubmitOps echoes a valid epoch.
  const [reconnectKey, setReconnectKey] = createSignal(0)

  let socket: WebSocket | undefined
  // Cleanup hooks for the Tauri sidecar relay path; null in browser mode.
  let bridgeCleanup: (() => void) | undefined
  // Shared exponential-backoff helper (jitter restored, vs the previous
  // hand-rolled `base * 2 ** attempts`) replacing the per-attempt counter.
  const reconnectBackoff = createExponentialBackoff<string>({
    initialMs: RECONNECT_BASE_DELAY_MS,
    maxMs: RECONNECT_MAX_DELAY_MS,
  })
  // Tracks whether a reconnect timer is armed but not yet fired, so tearDown can
  // distinguish an abandoned pending retry (reset the backoff streak) from the
  // fired retry that is itself re-running this effect (keep the streak growing).
  let reconnectPending = false
  let connectionGeneration = 0
  let disposed = false

  const decodeFrame = (raw: Uint8Array): WatchUserEvent | null => {
    // Length-prefixed frame: 4-byte BE uint32 + payload bytes. The
    // hub never sends multi-frame packing on the same WS message,
    // so a single message carries exactly one event. Same codec as
    // channelFraming (channel WebSocket frames).
    const framed = unframeBytes(raw)
    if (!framed.ok) {
      if (framed.failure.kind === 'short') {
        log.warn('dropping userevents frame shorter than its length prefix', { length: framed.failure.length })
      }
      else {
        log.warn('dropping userevents frame with a mismatched length prefix', {
          declared: framed.failure.declared,
          actual: framed.failure.actual,
        })
      }
      return null
    }
    try {
      return fromBinary(WatchUserEventSchema, framed.payload)
    }
    catch {
      return null
    }
  }

  const tearDown = () => {
    connectionGeneration++
    // Cancel a still-pending reconnect (userId change, manual reconnect, dispose)
    // and drop its backoff streak. A retry that already fired cleared
    // reconnectPending before re-running this effect, so its grown delay is
    // preserved for the next attempt; only a genuinely pending (abandoned) timer
    // is reset here.
    if (reconnectPending) {
      reconnectBackoff.reset(RECONNECT_KEY)
      reconnectPending = false
    }
    const closingSocket = socket
    socket = undefined
    if (closingSocket) {
      try {
        closingSocket.close()
      }
      catch {}
    }
    if (bridgeCleanup) {
      bridgeCleanup()
      bridgeCleanup = undefined
    }
    setBootstrapped(false)
  }

  const scheduleReconnect = (userId: string, generation: number) => {
    if (disposed || generation !== connectionGeneration || opts.userId() !== userId)
      return
    // schedule() no-ops when a timer is already armed for this key, so a paired
    // close+error for one disconnect still yields a single retry. It grows the
    // delay per attempt and adds jitter; a successful bootstrap resets it.
    reconnectPending = true
    reconnectBackoff.schedule(RECONNECT_KEY, () => {
      reconnectPending = false
      if (!disposed && generation === connectionGeneration && opts.userId() === userId)
        setReconnectKey(key => key + 1)
    })
  }

  // The effect depends on both userId AND reconnectKey so that
  // calling `reconnect()` re-runs the WebSocket setup even when the
  // user's identity hasn't changed.
  // readyGate defaults to always-true when the caller doesn't wire hydration,
  // preserving the prior "open as soon as userId is truthy" behavior. Included
  // in the WS effect's deps so a late hydration completion re-fires the effect
  // and opens the socket against the now-hydrated confirmedState.
  const readyGate = opts.ready ?? (() => true)
  createEffect(on([opts.userId, reconnectKey, readyGate], ([userId]) => {
    tearDown()
    if (!userId)
      return
    // Hydration gate: do not open the socket (or resolve the resume cursor)
    // until useCrdtRuntime signals the persisted checkpoint + op-log have
    // been loaded and replayed. Without this, a cold reload would resolve
    // the cursor against empty confirmedState and force a full snapshot even
    // when a valid checkpoint exists.
    if (!readyGate())
      return
    const generation = connectionGeneration

    // Resolve the resume cursor ONCE per connect attempt from the per-user
    // persisted watermark. The hub decides RESUME vs FALLBACK server-side from
    // this; a missing/stale cursor just yields a full snapshot (today's
    // behavior). Resolved here (not inside each transport branch) so the native
    // WS URL and the desktop sidecar RPC carry the same token.
    //
    // Refresh guard: a delta is only correct folded onto NON-EMPTY confirmed
    // state (the delta is a catch-up over the current visible set, not a full
    // replacement). Across a page refresh/restart, useCrdtRuntime now hydrates
    // confirmedState from the persisted checkpoint + op-log (checkpointStore.ts)
    // BEFORE this effect opens the socket (the `ready` gate), so confirmedState
    // is populated on a cold reload too and a delta lands on a non-empty base.
    // When hydration found no checkpoint (or the store is unavailable / the
    // pair failed to parse and was wiped), confirmedState stays empty and the
    // guard suppresses the cursor — sending it would make the hub resume a
    // delta the client folds onto empty maps, yielding partial state. So only
    // send the cursor when the live confirmedState is actually populated.
    const pendingMgr = opts.pending()
    const pendingState = pendingMgr?.state
    // A delta is only correct folded onto NON-EMPTY confirmed state, and the
    // populated check is authored on PendingOpsManager (next to the state whose
    // shape it describes) so it stays correct as the schema grows. Null
    // manager (briefly, before the userId effect constructs it) counts as
    // empty — a cold start.
    // The cursor is the manager's IN-MEMORY watermark, and only that.
    //
    // A cold reload gets it from the IndexedDB checkpoint (useCrdtRuntime gates
    // this effect on hydration completing), and an in-session reconnect from
    // live state -- so there is no case where confirmedState is populated but
    // the watermark is missing, and hence nothing for a second persisted copy
    // to rescue. There used to be one in localStorage; it could never be the
    // source, because the token below requires confirmedPopulated and every
    // path that populates confirmedState also seeds the watermark.
    //
    // The confirmedPopulated guard stays: a cursor is only meaningful if the
    // state it describes is actually loaded, or the hub's delta would fold onto
    // empty maps.
    const confirmedPopulated = pendingMgr?.isConfirmedPopulated() ?? false
    // A NARROWED subscription presents NO cursor.
    //
    // The persisted cursor is per-USER, not per-filter (see the hub's
    // SubscribeWithACL), and since the checkpoint seed landed it is CROSS-TAB
    // too -- a new tab adopts a sibling's. So a cursor minted under one filter
    // and replayed under a wider one can miss ops, and with cross-tab cursors
    // two tabs need not even agree on what the filter was. Suppressing the
    // cursor costs one full snapshot and removes the URL half of that pairing.
    // It is also what both in-tree Go narrowing callers already do: remoteipc's
    // hub_stream and the remote CLI client each pass their workspace ids with a
    // nil cursor.
    //
    // It is NOT the whole invariant -- a narrowed tab's CHECKPOINT still carries
    // the hub's global max_hlc over a filtered entity set, and a sibling can
    // adopt it. See allowedWorkspaceIds' doc for what a producer would also need.
    const workspaceIds = opts.allowedWorkspaceIds?.() ?? []
    const validated = confirmedPopulated && workspaceIds.length === 0
      ? validateResumeHlc(pendingState?.resumeWatermark)
      : undefined
    const resume: ResumeToken | null
      = pendingState && validated
        ? { hlc: validated, epoch: pendingState.currentEpoch }
        : null

    // Decode one relay frame and dispatch it, resetting the reconnect streak on
    // the bootstrap frame. Shared by the desktop-bridge and native WebSocket
    // transports so the bootstrap-frame backoff-reset rule can't drift between
    // them. A successful resume arrives as `delta` (not `initial`), but it is
    // equally a completed bootstrap, so the streak resets on either.
    const handleFrame = (raw: Uint8Array) => {
      const evt = decodeFrame(raw)
      if (!evt)
        return
      if (evt.event.case === 'initial' || evt.event.case === 'delta')
        reconnectBackoff.reset(RECONNECT_KEY)
      dispatchEvent(opts, evt, setBootstrapped, setClock)
    }

    // Desktop sidecar path: the webview can't open a native WS to
    // the unix-socket hub in solo mode, so the Go sidecar dials
    // `/ws/userevents` for us and forwards each frame as a Tauri
    // event. Skip this branch when a `buildWsUrl` override is
    // supplied (tests intentionally drive a real WebSocket).
    if (isTauriApp() && !opts.buildWsUrl) {
      // One id per attempt, shared by this attempt's open and its close, so the
      // sidecar can tell the two apart from a successor's.
      const relayId = nextUserEventsRelayId()
      let unsubMessage: (() => void) | undefined
      let unsubClose: (() => void) | undefined
      // Per-attempt cancellation flag for this async bridge setup, distinct
      // from the hook-level `disposed` above; named apart so an edit here
      // can't silently read the wrong scope's flag.
      let attemptDisposed = false
      // The bridge handlers need the same stale-connection guard the native WS
      // handlers carry (`socket !== ws || generation !== connectionGeneration`), and
      // they cannot rely on being unsubscribed instead: unsubMessage/unsubClose are
      // only assigned once the onEvent promises resolve, so between Rust registering
      // a listener and that microtask, bridgeCleanup marks the attempt disposed but
      // unsubscribes NOTHING. A close delivered in that window would otherwise reach
      // this superseded attempt's handler and tear down the generation that replaced
      // it -- firing AppShell's "Live updates disconnected" on a freshly-reconnected session.
      const isStaleAttempt = () => attemptDisposed || generation !== connectionGeneration
      // Open the relay, then attach event listeners. Order matters
      // less than for native WS — the sidecar buffers a few frames
      // on its own pending channel — but listening before open is
      // still safer because the initial UserMaterialized fires
      // immediately after Subscribe.
      Promise.all([
        platformBridge.onEvent('userevents:message', (b64) => {
          if (isStaleAttempt())
            return
          if (typeof b64 !== 'string')
            return
          handleFrame(base64ToUint8Array(b64))
        }),
        platformBridge.onEvent('userevents:close', (payload: unknown) => {
          if (isStaleAttempt())
            return
          setBootstrapped(false)
          const close = parseRelayClosePayload(payload)
          const code = close.code
          if (isTerminalCloseCode(code)) {
            // Terminal close: stop retrying AND release the bridge resources.
            // Unlike the native WS path -- whose listeners are GC'd once the
            // socket ref is dropped -- the platformBridge onEvent listeners and
            // the Go-side relay persist until explicitly torn down, so without
            // this a stale userevents:message listener survives (and a later
            // re-subscribe without a reload would double-dispatch frames).
            tearDown()
            opts.onFatalClose?.({ code, reason: close.reason })
            return
          }
          scheduleReconnect(userId, generation)
        }),
      ])
        .then(([m, c]) => {
          unsubMessage = m as () => void
          unsubClose = c as () => void
          if (attemptDisposed) {
            unsubMessage?.()
            unsubClose?.()
            return
          }
          return platformBridge.openUserEventsRelay(relayId, workspaceIds, resume)
        })
        .catch(() => {
          scheduleReconnect(userId, generation)
        })
      bridgeCleanup = () => {
        attemptDisposed = true
        unsubMessage?.()
        unsubClose?.()
        // Names the relay THIS attempt opened: the close and the successor's open are
        // separate RPCs the sidecar runs on unordered goroutines, so without the id a
        // close that lost the race tears down the successor's relay instead.
        platformBridge.closeUserEventsRelay(relayId).catch(() => {})
      }
      return
    }

    const builder = opts.buildWsUrl ?? defaultBuildWsUrl
    const url = builder(workspaceIds, resume)
    let ws: WebSocket
    try {
      ws = new WebSocket(url, ['userevents-relay'])
    }
    catch {
      scheduleReconnect(userId, generation)
      return
    }
    ws.binaryType = 'arraybuffer'
    socket = ws
    ws.addEventListener('message', (ev) => {
      // Same stale-connection guard the close/error handlers use: a frame
      // already queued on a socket that reconnect() (or teardown) has
      // superseded must not reach handleFrame -- a stale `initial` would
      // reset currentEpoch to the old snapshot's value the reconnect is
      // refreshing (re-arming the epoch loop) and a stale batch/presence
      // would be applied twice into the still-live PendingOpsManager.
      if (socket !== ws || generation !== connectionGeneration)
        return
      if (!(ev.data instanceof ArrayBuffer))
        return
      handleFrame(new Uint8Array(ev.data))
    })
    ws.addEventListener('close', (ev) => {
      if (socket !== ws || generation !== connectionGeneration)
        return
      socket = undefined
      setBootstrapped(false)
      if (isTerminalCloseCode(ev.code)) {
        // Terminal close: stop retrying, mirroring the bridge path's tearDown().
        // A preceding `error` on this same socket already armed scheduleReconnect
        // (it has no way to know a terminal-coded close is coming), so without
        // bumping connectionGeneration and clearing reconnectPending here that
        // timer fires ~one backoff later and resubscribes the very connection the
        // fatal close was meant to stop -- reconnecting underneath AppShell's
        // disconnect banner.
        tearDown()
        opts.onFatalClose?.({ code: ev.code, reason: ev.reason })
        return
      }
      scheduleReconnect(userId, generation)
    })
    ws.addEventListener('error', () => {
      if (socket !== ws || generation !== connectionGeneration)
        return
      setBootstrapped(false)
      scheduleReconnect(userId, generation)
      try {
        ws.close()
      }
      catch {}
    })
  }))

  onCleanup(() => {
    disposed = true
    tearDown()
    reconnectBackoff.cancelAll()
  })

  const reconnect = (): Promise<void> => {
    tearDown()
    setReconnectKey(k => k + 1)
    // The effect re-runs synchronously after the signal update; the
    // returned promise resolves immediately. Callers that need to know
    // when the next UserMaterialized has arrived should watch
    // `bootstrapped`.
    return Promise.resolve()
  }

  return { bootstrapped, clock, reconnect }
}

// defaultBuildWsUrl mirrors the /ws/userevents query-string shape that Go's
// channelwire.UserEventsURL (backend/channelwire/wire.go) is the source of
// truth for: optional comma-joined `workspace_ids` only. The authenticated
// session implies the user — no user_id query parameter. The browser cannot
// import Go, so it keeps its own copy -- like channel.ts's channel framing --
// and the two must stay in lockstep.
//
// `resume`, when present, appends `resume_after_hlc=<physical>.<logical>.<client_id>`
// (via the shared formatHlcWire so the wire shape is authored once per language)
// and `resume_epoch=<epoch>` so the hub can attempt a delta resume. The hub
// falls back to a full snapshot if the cursor is stale, so sending it is always
// safe; omitted when there is no watermark (first connect, cleared storage).
function defaultBuildWsUrl(workspaceIds: string[], resume: ResumeToken | null): string {
  const base = window.location.origin.replace(/^http/, 'ws')
  const params = new URLSearchParams()
  if (workspaceIds.length > 0)
    params.set('workspace_ids', workspaceIds.join(','))
  if (resume) {
    params.set('resume_after_hlc', formatHlcWire(resume.hlc))
    params.set('resume_epoch', resume.epoch.toString())
  }
  const qs = params.toString()
  return `${base}/ws/userevents${qs ? `?${qs}` : ''}`
}

function dispatchEvent(
  opts: UseUserEventsOpts,
  evt: WatchUserEvent,
  setBootstrapped: (b: boolean) => void,
  setClock: (c: HLCClock | null) => void,
): void {
  const e = evt.event
  // Resolve the manager ONCE. Exactly one arm runs per call, and this executes
  // from a WS listener with no tracking scope, so re-reading the accessor per
  // arm (five copies of `const pending = opts.pending()`) bought nothing. The
  // per-arm `if (pending)` guards stay: the manager is null until the userId
  // effect constructs it, and `presence` / `created` / `renamed` / `deleted`
  // must keep running while it is.
  const pending = opts.pending()
  switch (e.case) {
    case 'initial':
      // Only adopt when the payload was actually accepted. A frame refused for
      // naming another tenant must not reach the adoption tail: downstream
      // reads `bootstrapped` as "the CRDT is live", so a refused bootstrap
      // would look identical to a successful one while the shell held no state
      // at all.
      if (pending && applyMaterialized(opts, pending, e.value))
        adoptBootstrapFrame(opts, pending, e.value.subscriberClientId, setBootstrapped, setClock)
      break
    case 'delta': {
      // A successful resume: the hub shipped the ordered frame stream (the
      // post-cursor tail plus materialized/removed visibility transitions)
      // instead of a full snapshot. applyDelta folds it into the EXISTING
      // confirmedState (no wholesale replace), so the UI does not blank on
      // reconnect. The hub falls back to `initial` (handled above) when the
      // cursor is at/below the compaction watermark or the epoch is stale, in
      // which case applyMaterialized re-seeds the watermark from the snapshot.
      if (!pending)
        break
      // Refuse a delta that is not this tenant's, the same fail-closed check
      // applyMaterialized applies to `initial` and loadHydrationState applies
      // to the persisted checkpoint. A resume is now the normal cold start
      // and was the only one of the three adoption points that failed open.
      // The per-socket generation guard answers "is this frame stale?", not
      // "is this frame mine?".
      if (!isOwnTenant(opts.userId(), e.value.userId, 'resume delta'))
        break
      if (pending.applyDelta(e.value).droppedPending)
        opts.onPendingDropped?.()
      // Same adoption tail as the `initial` arm, and deliberately the same
      // CALL: a resume is the normal cold start now, so anything `initial`
      // establishes has to be established here too. The two arms hand-copied
      // this sequence and had already drifted apart twice.
      adoptBootstrapFrame(opts, pending, e.value.subscriberClientId, setBootstrapped, setClock)
      break
    }
    case 'batch':
      if (pending)
        pending.consumeRemote(e.value)
      break
    case 'entityMaterialized':
      if (pending)
        pending.consumeEntityMaterialized(e.value)
      break
    case 'batchEnd':
      // Closes a batch's frame sequence and is the ONLY point the resume
      // cursor advances -- see the BatchEnd proto doc.
      if (pending)
        pending.consumeBatchEnd(e.value.atHlc)
      break
    case 'entityRemoved':
      if (pending && pending.consumeEntityRemoved(e.value).droppedPending)
        opts.onPendingDropped?.()
      break
    case 'presence':
      opts.activeClient.update(e.value.workspaceId, e.value.activeClientId)
      break
    case 'created':
      // Surface a window-level event so awaiters (e.g.
      // `seedTabIntoNewWorkspace` in `NewWorkspaceDialog`) can react
      // to the new workspace becoming visible without polling the
      // speculative state. Dispatched BEFORE the lifecycle-changed
      // callback so awaiters fire in the same microtask the sidebar
      // refresh kicks off — order matters because seed-tab batches
      // need to land before the registry refresh re-renders an empty
      // workspace.
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent('leapmux:workspace-created', {
          detail: {
            workspaceId: e.value.workspaceId,
            rootNodeId: e.value.rootNodeId,
            title: e.value.title,
          },
        }))
      }
      opts.onWorkspaceLifecycleChanged?.()
      break
    case 'renamed':
    case 'deleted':
      // Workspace-lifecycle events trigger a sidebar refresh; the
      // pending manager doesn't need to act on them itself. AppShell
      // hands us a refresh callback that re-runs `listWorkspaces` so
      // the sidebar picks up the new workspace's title without
      // requiring a reconnect.
      opts.onWorkspaceLifecycleChanged?.()
      break
  }
}

/**
 * The tail both bootstrap-bearing frames run once their payload is accepted:
 * an `initial` UserMaterialized and a `delta` ResumeDelta.
 *
 * ONE sequence, in one place, because the two arms establish the same four
 * things and a resume is now the normal cold start — a client that keeps
 * resuming may never see an `initial` frame at all. Hand-copied, the tail had
 * already drifted twice: the delta arm reached production without adopting the
 * hub's subscriber identity (so the active-client gate compared against '' for
 * the life of the page) and without the workspace-list refresh.
 *
 * `subscriberClientId` is empty when the hub named none; proto3 normalizes an
 * unset string to "", and adopting "" would overwrite a good identity with
 * nothing.
 */
function adoptBootstrapFrame(
  opts: UseUserEventsOpts,
  pending: PendingOpsManager,
  subscriberClientId: string,
  setBootstrapped: (b: boolean) => void,
  setClock: (c: HLCClock | null) => void,
): void {
  // The hub derives this from the authenticated session; the active-client gate
  // compares broadcast `active_client_id` against it, never against the local
  // random nanoid.
  if (subscriberClientId)
    opts.onSubscriberClientId?.(subscriberClientId)
  setClock(pending.clock)
  setBootstrapped(true)
  // Refresh the workspace list on EVERY bootstrap. Workspace lifecycle
  // (`created`/`renamed`/`deleted`) is delivered as its own frame on the live
  // stream, and the sidebar list comes from a separate `listWorkspaces` call
  // driven solely by this callback — so a create, rename or delete that
  // happened while this client was disconnected reaches it through NEITHER
  // path: the gap's lifecycle frames were never sent, and neither
  // `UserMaterialized` nor `ResumeDelta` carries them (ResumeDelta carries
  // entity_materialized|batch|entity_removed|batch_end -- entity data and batch
  // boundaries, never workspace lifecycle). Without this, a reconnect leaves
  // the sidebar showing a deleted workspace, a stale title, or missing a new
  // one indefinitely.
  opts.onWorkspaceLifecycleChanged?.()
}

/**
 * Install an `initial` snapshot into `pending`. Returns true when the payload
 * was adopted; false when it was refused for naming another tenant, in which
 * case the caller must NOT run the adoption tail.
 *
 * Takes the already-resolved manager rather than re-reading `opts.pending()`:
 * dispatchEvent resolves it once for every arm.
 */
function applyMaterialized(
  opts: UseUserEventsOpts,
  pending: PendingOpsManager,
  materialized: UserMaterialized,
): boolean {
  // Refuse a payload that names another tenant. UserMaterialized carries its
  // OWN user_id, so adopting it unconditionally would let the frame -- not the
  // socket it arrived on -- decide whose workspaces, tiles and tabs this shell
  // renders. The hub added exactly this check on its side for the same payload
  // (crdt.Manager.requireOwnState); this is the client half, and without it
  // the only protection was the per-socket generation guard, which answers
  // "is this frame stale?" and not "is this frame mine?".
  //
  // Fail closed: an unknown local id refuses rather than adopts.
  if (!isOwnTenant(opts.userId(), materialized.userId, 'materialized payload'))
    return false
  pending.bootstrap({
    userId: materialized.userId,
    nodes: materialized.nodes as never,
    tabs: materialized.tabs as never,
    floatingWindows: materialized.floatingWindows as never,
    workspaces: materialized.workspaces,
    maxHlc: materialized.maxHlc as never,
    currentEpoch: materialized.currentEpoch,
  })
  return true
}
