import type { OrgMaterialized } from '~/generated/leapmux/v1/org_crdt_pb'
import type { WatchOrgEvent } from '~/generated/leapmux/v1/org_ops_pb'
import type { HLCClock, PendingOpsManager } from '~/lib/crdt'
import type { ActiveClientStore } from '~/lib/presence/activeClient'
import { fromBinary } from '@bufbuild/protobuf'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { WatchOrgEventSchema } from '~/generated/leapmux/v1/org_ops_pb'
import { base64ToUint8Array } from '~/lib/base64'

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
   * Called when WorkspaceCreated / WorkspaceRenamed / WorkspaceDeleted
   * arrives. The callback typically re-fetches the org's workspace
   * list so the sidebar reflects the lifecycle change. Routed through
   * a single hook so the workspace store, section store, and registry
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

  const decodeFrame = (raw: Uint8Array): WatchOrgEvent | null => {
    // Length-prefixed frame: 4-byte BE uint32 + payload bytes. The
    // hub never sends multi-frame packing on the same WS message,
    // so a single message carries exactly one event.
    if (raw.length < 4)
      return null
    const len = (raw[0] << 24) | (raw[1] << 16) | (raw[2] << 8) | raw[3]
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
    if (socket) {
      try {
        socket.close()
      }
      catch {}
      socket = undefined
    }
    if (bridgeCleanup) {
      bridgeCleanup()
      bridgeCleanup = undefined
    }
    setBootstrapped(false)
  }

  // The effect depends on both orgId AND reconnectKey so that
  // calling `reconnect()` re-runs the WebSocket setup even when the
  // user's org hasn't changed.
  createEffect(on([opts.orgId, reconnectKey], ([orgId]) => {
    tearDown()
    if (!orgId)
      return

    // Desktop sidecar path: the webview can't open a native WS to
    // the unix-socket hub in solo mode, so the Go sidecar dials
    // `/ws/orgevents` for us and forwards each frame as a Tauri
    // event. Skip this branch when a `buildWsUrl` override is
    // supplied (tests intentionally drive a real WebSocket).
    if (isTauriApp() && !opts.buildWsUrl) {
      const workspaceIds = opts.allowedWorkspaceIds?.() ?? []
      let unsubMessage: (() => void) | undefined
      let unsubClose: (() => void) | undefined
      let disposed = false
      // Open the relay, then attach event listeners. Order matters
      // less than for native WS — the sidecar buffers a few frames
      // on its own pending channel — but listening before open is
      // still safer because the initial OrgMaterialized fires
      // immediately after Subscribe.
      Promise.all([
        platformBridge.onEvent('orgevents:message', (b64) => {
          if (typeof b64 !== 'string')
            return
          const raw = base64ToUint8Array(b64)
          const evt = decodeFrame(raw)
          if (evt)
            dispatchEvent(opts, evt, setBootstrapped, setClock)
        }),
        platformBridge.onEvent('orgevents:close', () => {
          setBootstrapped(false)
        }),
      ])
        .then(([m, c]) => {
          unsubMessage = m as () => void
          unsubClose = c as () => void
          if (disposed) {
            unsubMessage?.()
            unsubClose?.()
            return
          }
          return platformBridge.openOrgEventsRelay(orgId, workspaceIds)
        })
        .catch(() => {
          // openOrgEventsRelay failures surface as the orgevents:close
          // event from the sidecar; nothing else to do here.
        })
      bridgeCleanup = () => {
        disposed = true
        unsubMessage?.()
        unsubClose?.()
        platformBridge.closeOrgEventsRelay().catch(() => {})
      }
      return
    }

    const url = opts.buildWsUrl
      ? opts.buildWsUrl(orgId, opts.allowedWorkspaceIds?.() ?? [])
      : defaultBuildWsUrl(orgId, opts.allowedWorkspaceIds?.() ?? [])
    const ws = new WebSocket(url, ['orgevents-relay'])
    ws.binaryType = 'arraybuffer'
    socket = ws
    ws.addEventListener('message', (ev) => {
      if (!(ev.data instanceof ArrayBuffer))
        return
      const evt = decodeFrame(new Uint8Array(ev.data))
      if (evt)
        dispatchEvent(opts, evt, setBootstrapped, setClock)
    })
    ws.addEventListener('close', () => {
      // Closed: the outer effect will re-open on the next orgId tick
      // (e.g. user signs back in). For now just clear bootstrap.
      setBootstrapped(false)
    })
    ws.addEventListener('error', () => {
      // Errors close automatically on most browsers; treat as a clean
      // close so the reconnect logic in the parent effect can re-open.
    })
  }))

  onCleanup(tearDown)

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
