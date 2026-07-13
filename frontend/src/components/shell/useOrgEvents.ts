import type { OrgMaterialized } from '~/generated/leapmux/v1/org_crdt_pb'
import type { WatchOrgEvent } from '~/generated/leapmux/v1/org_ops_pb'
import type { HLCClock, PendingOpsManager } from '~/lib/crdt'
import type { ActiveClientStore } from '~/lib/presence/activeClient'
import { fromBinary } from '@bufbuild/protobuf'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { WatchOrgEventSchema } from '~/generated/leapmux/v1/org_ops_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { createExponentialBackoff } from '~/lib/retry'

const RECONNECT_BASE_DELAY_MS = 250
const RECONNECT_MAX_DELAY_MS = 5_000
// Single-key backoff: the hook drives one connection at a time, so one key is
// enough to escalate the reconnect delay across attempts and reset it on success.
const RECONNECT_KEY = 'orgevents'

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
   * Called when workspace lifecycle or visibility changes arrive. The
   * callback typically re-fetches the org's workspace list so the sidebar
   * reflects creates, renames, deletes, grants, and revocations. Routed
   * through a single hook so the workspace store, section store, and registry
   * each get their refresh in one place.
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
    if (raw.length < 4)
      return null
    // `>>> 0` forces an unsigned 32-bit length: a high top byte (frame >= 2 GiB)
    // would otherwise sign-extend to a negative length that slips past the
    // bounds check below and yields a garbage subarray.
    const len = ((raw[0] << 24) | (raw[1] << 16) | (raw[2] << 8) | raw[3]) >>> 0
    if (4 + len > raw.length)
      return null
    try {
      return fromBinary(WatchOrgEventSchema, raw.subarray(4, 4 + len))
    }
    catch {
      return null
    }
  }

  const tearDown = () => {
    connectionGeneration++
    // Cancel a still-pending reconnect (org switch, manual reconnect, dispose)
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
      let unsubMessage: (() => void) | undefined
      let unsubClose: (() => void) | undefined
      // Per-attempt cancellation flag for this async bridge setup, distinct
      // from the hook-level `disposed` above; named apart so an edit here
      // can't silently read the wrong scope's flag.
      let attemptDisposed = false
      // Open the relay, then attach event listeners. Order matters
      // less than for native WS — the sidecar buffers a few frames
      // on its own pending channel — but listening before open is
      // still safer because the initial OrgMaterialized fires
      // immediately after Subscribe.
      Promise.all([
        platformBridge.onEvent('orgevents:message', (b64) => {
          if (typeof b64 !== 'string')
            return
          handleFrame(base64ToUint8Array(b64))
        }),
        platformBridge.onEvent('orgevents:close', () => {
          setBootstrapped(false)
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
          return platformBridge.openOrgEventsRelay(orgId, workspaceIds)
        })
        .catch(() => {
          scheduleReconnect(orgId, generation)
        })
      bridgeCleanup = () => {
        attemptDisposed = true
        unsubMessage?.()
        unsubClose?.()
        platformBridge.closeOrgEventsRelay().catch(() => {})
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
    ws.addEventListener('close', () => {
      if (socket !== ws || generation !== connectionGeneration)
        return
      socket = undefined
      setBootstrapped(false)
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
    case 'workspaceProjection': {
      const pending = opts.pending()
      if (pending) {
        const result = pending.consumeWorkspaceProjection(e.value)
        if (result.droppedPending)
          opts.onPendingDropped?.()
      }
      opts.onWorkspaceLifecycleChanged?.()
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
