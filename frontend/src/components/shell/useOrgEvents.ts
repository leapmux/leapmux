import type { OrgMaterialized } from '~/generated/leapmux/v1/org_crdt_pb'
import type { WatchOrgEvent } from '~/generated/leapmux/v1/org_ops_pb'
import type { HLCClock, PendingOpsManager } from '~/lib/crdt'
import type { ActiveClientStore } from '~/lib/presence/activeClient'
import { fromBinary } from '@bufbuild/protobuf'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { isTauriApp, parseRelayClosePayload, platformBridge } from '~/api/platformBridge'
import { WatchOrgEventSchema } from '~/generated/leapmux/v1/org_ops_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { KEY_ORG_EVENTS_RELAY_SEQ } from '~/lib/browserStorage'
import { createLogger } from '~/lib/logger'
import { createPersistedSeq } from '~/lib/persistedSeq'
import { createExponentialBackoff } from '~/lib/retry'

const log = createLogger('useOrgEvents')

const RECONNECT_BASE_DELAY_MS = 250
const RECONNECT_MAX_DELAY_MS = 5_000
// Single-key backoff: the hook drives one connection at a time, so one key is
// enough to escalate the reconnect delay across attempts and reset it on success.
const RECONNECT_KEY = 'orgevents'

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
  1008, // policy violation -- the hub's /ws/orgevents "forbidden" / auth expiry
])

function isTerminalCloseCode(code: number): boolean {
  return TERMINAL_CLOSE_CODES.has(code)
}

// Ids for the desktop sidecar's org-events relay, handed out in dispatch order: the
// sidecar compares them to ignore a close whose relay a later open already replaced,
// and to ignore an open a later one has superseded. A stale-looking open matters
// here because the hub only sends OrgMaterialized at subscribe time -- a dropped
// open means org events silently never bootstrap. The persisted clock-seeded
// sequence (shared with the channel relay's claim ids -- see createPersistedSeq
// for the reload/clock-regression rationale) keeps a fresh page's ids above
// whatever the still-live sidecar already holds.
/** Exported for tests; production code reaches it only through useOrgEvents. */
export const nextOrgEventsRelayId = createPersistedSeq(KEY_ORG_EVENTS_RELAY_SEQ)

/**
 * useOrgEvents opens a single per-org WebSocket connection at
 * `/ws/orgevents?org_id=...&workspace_ids=...` and dispatches every
 * incoming `WatchOrgEvent` frame into the local PendingOpsManager /
 * ActiveClientStore.
 *
 * **WebSocket transport rationale**: HTTP/1.1 chunked streaming over
 * intermediaries (corporate proxies, the desktop sidecar's Tauri
 * proxy) is unreliable — buffers can hold the body until upstream
 * close, which would freeze a long-lived event stream. WebSocket
 * negotiates Upgrade and bypasses those buffers. The wire format is
 * `[4-byte big-endian length][protobuf-encoded WatchOrgEvent]` per
 * binary frame, mirroring the `/ws/channel` E2EE relay so a single
 * read helper handles both endpoints.
 *
 * The hook stays alive for the lifetime of the org session — when
 * the user switches workspaces, the same connection keeps running
 * and the layered projection is sliced per-workspace from the
 * materialized state. Reconnects re-bootstrap; clients dedup echoed
 * batches via `batch_id`.
 */
export interface UseOrgEventsOpts {
  /** Reactive accessor for the org id. Empty disables the connection. */
  orgId: () => string
  /** Reactive accessor for workspace_ids the caller may read; empty array = all. */
  allowedWorkspaceIds?: () => string[]
  /** Active-client store fed by PresenceUpdate events. */
  activeClient: ActiveClientStore
  /** PendingOpsManager that owns confirmed + speculative state. */
  pending: () => PendingOpsManager | null
  /** Optional override for the WebSocket URL builder (tests). */
  buildWsUrl?: (orgId: string, workspaceIds: string[]) => string
  /** Called when an EntityRemoved drops a pending op (caller may toast). */
  onPendingDropped?: () => void
  /**
   * Called when workspace lifecycle changes arrive. The callback typically
   * re-fetches the org's workspace list so the sidebar reflects creates,
   * renames, and deletes. Routed through a single hook so the workspace
   * store, section store, and registry each get their refresh in one place.
   */
  onWorkspaceLifecycleChanged?: () => void
  /**
   * Called whenever the hub tells us our effective subscriber identity
   * via `OrgMaterialized.subscriber_client_id`. The hub derives this
   * from the authenticated session (or bearer token id) — the active-
   * client gate compares broadcast `active_client_id` against this,
   * NOT against the local random nanoid, because the hub stamps
   * `PresenceUpdate.active_client_id` from the same session identity
   * and the two would never otherwise match. Fired once per bootstrap.
   */
  onSubscriberClientId?: (clientId: string) => void
  /**
   * Called when the org-events stream closes with a terminal code (an
   * authorization/protocol failure, e.g. auth expiry) where auto-reconnect is
   * futile. The hook stops retrying and hands the caller the close code/reason
   * so it can surface a toast/banner (e.g. prompt a reload or re-auth) instead
   * of looping. Recoverable/transient closes reconnect silently and never fire
   * this.
   */
  onFatalClose?: (info: { code: number, reason: string }) => void
}

export interface OrgEventsHook {
  /** True once the initial OrgMaterialized has been received. */
  bootstrapped: () => boolean
  /** HLC clock the local pending manager uses. */
  clock: () => HLCClock | null
  /**
   * Force a teardown + fresh subscribe. Used by `useOpsSubmitter` when
   * the hub rejects a SubmitOps batch as `epoch_required` or
   * `stale_epoch` — the client must refresh `currentEpoch` via a new
   * `OrgMaterialized` before retrying. Returns a promise that resolves
   * once the WS has been closed; the next bootstrap event arrives
   * asynchronously on the new connection.
   */
  reconnect: () => Promise<void>
}

export function useOrgEvents(opts: UseOrgEventsOpts): OrgEventsHook {
  const [bootstrapped, setBootstrapped] = createSignal(false)
  const [clock, setClock] = createSignal<HLCClock | null>(null)
  // reconnectKey forces the orgId effect to re-fire even when orgId
  // hasn't changed — used by `reconnect()` to drop the current WS
  // and start a fresh subscription. The new OrgMaterialized refreshes
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

  const decodeFrame = (raw: Uint8Array): WatchOrgEvent | null => {
    // Length-prefixed frame: 4-byte BE uint32 + payload bytes. The
    // hub never sends multi-frame packing on the same WS message,
    // so a single message carries exactly one event.
    if (raw.length < 4) {
      log.warn('dropping org-events frame shorter than its length prefix', { length: raw.length })
      return null
    }
    // `>>> 0` forces an unsigned 32-bit length: a high top byte (frame >= 2 GiB)
    // would otherwise sign-extend to a negative length that slips past the
    // bounds check below and yields a garbage subarray.
    const len = ((raw[0] << 24) | (raw[1] << 16) | (raw[2] << 8) | raw[3]) >>> 0
    // Exact match, mirroring channel.ts's strict framing check: a frame with
    // trailing bytes is a protocol violation, and quietly decoding its prefix
    // would mask a hub<->frontend framing desync until it became undebuggable.
    if (len !== raw.length - 4) {
      log.warn('dropping org-events frame with a mismatched length prefix', { declared: len, actual: raw.length - 4 })
      return null
    }
    try {
      return fromBinary(WatchOrgEventSchema, raw.subarray(4, 4 + len))
    }
    catch {
      return null
    }
  }

  const tearDown = () => {
    connectionGeneration++
    // Cancel a still-pending reconnect (orgId change, manual reconnect, dispose)
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

  const scheduleReconnect = (orgId: string, generation: number) => {
    if (disposed || generation !== connectionGeneration || opts.orgId() !== orgId)
      return
    // schedule() no-ops when a timer is already armed for this key, so a paired
    // close+error for one disconnect still yields a single retry. It grows the
    // delay per attempt and adds jitter; a successful bootstrap resets it.
    reconnectPending = true
    reconnectBackoff.schedule(RECONNECT_KEY, () => {
      reconnectPending = false
      if (!disposed && generation === connectionGeneration && opts.orgId() === orgId)
        setReconnectKey(key => key + 1)
    })
  }

  // The effect depends on both orgId AND reconnectKey so that
  // calling `reconnect()` re-runs the WebSocket setup even when the
  // user's org hasn't changed.
  createEffect(on([opts.orgId, reconnectKey], ([orgId]) => {
    tearDown()
    if (!orgId)
      return
    const generation = connectionGeneration

    // Decode one relay frame and dispatch it, resetting the reconnect streak on
    // the bootstrap (initial) frame. Shared by the desktop-bridge and native
    // WebSocket transports so the initial-frame backoff-reset rule can't drift
    // between them.
    const handleFrame = (raw: Uint8Array) => {
      const evt = decodeFrame(raw)
      if (!evt)
        return
      if (evt.event.case === 'initial')
        reconnectBackoff.reset(RECONNECT_KEY)
      dispatchEvent(opts, evt, setBootstrapped, setClock)
    }

    // Desktop sidecar path: the webview can't open a native WS to
    // the unix-socket hub in solo mode, so the Go sidecar dials
    // `/ws/orgevents` for us and forwards each frame as a Tauri
    // event. Skip this branch when a `buildWsUrl` override is
    // supplied (tests intentionally drive a real WebSocket).
    if (isTauriApp() && !opts.buildWsUrl) {
      const workspaceIds = opts.allowedWorkspaceIds?.() ?? []
      // One id per attempt, shared by this attempt's open and its close, so the
      // sidecar can tell the two apart from a successor's.
      const relayId = nextOrgEventsRelayId()
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
      // it -- firing AppShell's "Live updates disconnected" on a freshly-switched org.
      const isStaleAttempt = () => attemptDisposed || generation !== connectionGeneration
      // Open the relay, then attach event listeners. Order matters
      // less than for native WS — the sidecar buffers a few frames
      // on its own pending channel — but listening before open is
      // still safer because the initial OrgMaterialized fires
      // immediately after Subscribe.
      Promise.all([
        platformBridge.onEvent('orgevents:message', (b64) => {
          if (isStaleAttempt())
            return
          if (typeof b64 !== 'string')
            return
          handleFrame(base64ToUint8Array(b64))
        }),
        platformBridge.onEvent('orgevents:close', (payload: unknown) => {
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
            // this a stale orgevents:message listener survives (and a later
            // re-subscribe without a reload would double-dispatch frames).
            tearDown()
            opts.onFatalClose?.({ code, reason: close.reason })
            return
          }
          scheduleReconnect(orgId, generation)
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
          return platformBridge.openOrgEventsRelay(relayId, orgId, workspaceIds)
        })
        .catch(() => {
          scheduleReconnect(orgId, generation)
        })
      bridgeCleanup = () => {
        attemptDisposed = true
        unsubMessage?.()
        unsubClose?.()
        // Names the relay THIS attempt opened: the close and the successor's open are
        // separate RPCs the sidecar runs on unordered goroutines, so without the id a
        // close that lost the race tears down the successor's relay instead.
        platformBridge.closeOrgEventsRelay(relayId).catch(() => {})
      }
      return
    }

    const url = opts.buildWsUrl
      ? opts.buildWsUrl(orgId, opts.allowedWorkspaceIds?.() ?? [])
      : defaultBuildWsUrl(orgId, opts.allowedWorkspaceIds?.() ?? [])
    let ws: WebSocket
    try {
      ws = new WebSocket(url, ['orgevents-relay'])
    }
    catch {
      scheduleReconnect(orgId, generation)
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
      scheduleReconnect(orgId, generation)
    })
    ws.addEventListener('error', () => {
      if (socket !== ws || generation !== connectionGeneration)
        return
      setBootstrapped(false)
      scheduleReconnect(orgId, generation)
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
    // when the next OrgMaterialized has arrived should watch
    // `bootstrapped`.
    return Promise.resolve()
  }

  return { bootstrapped, clock, reconnect }
}

// defaultBuildWsUrl mirrors the /ws/orgevents query-string shape that Go's
// channelwire.OrgEventsURL (backend/channelwire/wire.go) is the source of truth
// for: an `org_id` param plus a comma-joined `workspace_ids`. The browser cannot
// import Go, so it keeps its own copy -- like channel.ts's channel framing -- and
// the two must stay in lockstep: a rename of a query key on the Go side has no
// compile-time or fixture check to catch a missed edit here.
function defaultBuildWsUrl(orgId: string, workspaceIds: string[]): string {
  const base = window.location.origin.replace(/^http/, 'ws')
  const params = new URLSearchParams({ org_id: orgId })
  if (workspaceIds.length > 0)
    params.set('workspace_ids', workspaceIds.join(','))
  return `${base}/ws/orgevents?${params.toString()}`
}

function dispatchEvent(
  opts: UseOrgEventsOpts,
  evt: WatchOrgEvent,
  setBootstrapped: (b: boolean) => void,
  setClock: (c: HLCClock | null) => void,
): void {
  const e = evt.event
  switch (e.case) {
    case 'initial':
      applyMaterialized(opts, e.value, setClock)
      setBootstrapped(true)
      break
    case 'batch': {
      const pending = opts.pending()
      if (pending)
        pending.consumeRemote(e.value)
      break
    }
    case 'entityMaterialized': {
      const pending = opts.pending()
      if (pending)
        pending.consumeEntityMaterialized(e.value)
      break
    }
    case 'entityRemoved': {
      const pending = opts.pending()
      if (pending) {
        const result = pending.consumeEntityRemoved(e.value)
        if (result.droppedPending)
          opts.onPendingDropped?.()
      }
      break
    }
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

function applyMaterialized(
  opts: UseOrgEventsOpts,
  materialized: OrgMaterialized,
  setClock: (c: HLCClock | null) => void,
): void {
  const pending = opts.pending()
  if (!pending)
    return
  pending.bootstrap({
    orgId: materialized.orgId,
    nodes: materialized.nodes as never,
    tabs: materialized.tabs as never,
    floatingWindows: materialized.floatingWindows as never,
    workspaces: materialized.workspaces,
    maxHlc: materialized.maxHlc as never,
    currentEpoch: materialized.currentEpoch,
  })
  if (materialized.subscriberClientId)
    opts.onSubscriberClientId?.(materialized.subscriberClientId)
  setClock(pending.clock)
}
