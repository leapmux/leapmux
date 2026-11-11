// ─────────────────────────────────────────────────────────────────────────
// E2E helpers for the long-lived UserCRDT event stream.
// Worker-owned tab creation publishes canonical records through the Hub.
// These subscriptions observe workspace roots and epochs across publication.
//
// The browser holds ONE long-lived `/ws/userevents` subscription per
// authenticated session and discovers `workspaces[wsID].rootNodeId` by
// absorbing events on it. The test fixtures mirror that: open the
// subscription BEFORE creating any workspace so the seed
// `SetNodeRegister` + `CreateWorkspace` ops broadcast through
// the production filter-expansion path. Opening a fresh subscription
// AFTER `createWorkspaceViaAPI` would mask a hub-side bug where the
// seed ops are dropped for existing subscribers — a real production
// regression that the fixture would silently hide.
// ─────────────────────────────────────────────────────────────────────────

import { fromBinary } from '@bufbuild/protobuf'
import { WatchUserEventSchema } from '../../../src/generated/proto/leapmux/v1/user_ops_pb'
import { hubTransportUrl } from './hubTransport'

const HTTP_TO_WS_RE = /^http/i

/**
 * UserEventsSubscription is the test-side analogue of the browser's
 * `useUserEvents` hook. It opens one long-lived `/ws/userevents`
 * WebSocket and accumulates workspace root and epoch observations:
 *
 *   - `workspaces[wsID].rootNodeId` — populated either by the
 *     initial `UserMaterialized` (for workspaces that existed at
 *     subscribe time) or by the raw `CreateWorkspace` op the
 *     hub broadcasts for workspaces created later (which itself
 *     requires the hub's filter-expansion fix).
 *   - `currentEpoch` — read from the initial frame, refreshed if
 *     ever surfaced again.
 *
 * To mirror the production race, callers MUST open the subscription
 * BEFORE `createWorkspaceViaAPI`. Calling `awaitRootNodeId` after a
 * workspace creation then verifies the entire seed-ops broadcast
 * path end-to-end.
 */
export interface UserEventsSubscription {
  /** Resolves with the workspace's rootNodeId once any inbound event sets it. */
  awaitRootNodeId: (workspaceId: string, timeoutMs?: number) => Promise<string>
  /** Best-effort current epoch (from initial bootstrap, refreshed if a later frame surfaces one). */
  currentEpoch: () => bigint
  /** Whether the underlying WebSocket has closed (locally or remotely). */
  isClosed: () => boolean
  close: () => void
}

/**
 * Open a long-lived `/ws/userevents` subscription and start
 * accumulating workspace root_node_ids from inbound events. Resolves
 * once the initial `UserMaterialized` frame lands so callers can rely
 * on a stable bootstrap before triggering further hub state changes.
 */
async function openUserEventsSubscription(
  hubUrl: string,
  cookie: string,
): Promise<UserEventsSubscription> {
  const wsUrl = `${hubUrl.replace(HTTP_TO_WS_RE, 'ws')}/ws/userevents`
  const transportUrl = hubTransportUrl(wsUrl)
  const ws = new WebSocket(transportUrl, {
    headers: { Cookie: cookie },
  } as any)
  ws.binaryType = 'arraybuffer'

  // workspaceId -> rootNodeId once observed.
  const roots = new Map<string, string>()
  // workspaceId -> waiters awaiting rootNodeId resolution.
  const waiters = new Map<string, Array<(rootNodeId: string) => void>>()
  let epoch = 0n
  let closed = false

  const setRoot = (wsId: string, rootNodeId: string) => {
    if (!wsId || !rootNodeId)
      return
    if (roots.get(wsId) === rootNodeId)
      return
    roots.set(wsId, rootNodeId)
    const pending = waiters.get(wsId)
    if (pending) {
      waiters.delete(wsId)
      for (const fn of pending)
        fn(rootNodeId)
    }
  }

  const close = () => {
    if (closed)
      return
    closed = true
    try {
      ws.close()
    }
    catch {
      // best-effort
    }
  }

  await new Promise<void>((resolve, reject) => {
    const openTimer = setTimeout(() => {
      close()
      reject(new Error('openUserEventsSubscription: timeout waiting for initial UserMaterialized'))
    }, 10_000)

    ws.addEventListener('error', () => {
      clearTimeout(openTimer)
      close()
      reject(new Error('openUserEventsSubscription: WS error'))
    })

    ws.addEventListener('message', (ev) => {
      const buf = ev.data instanceof ArrayBuffer
        ? new Uint8Array(ev.data)
        : null
      if (!buf || buf.length < 4)
        return
      let len: number
      let evt: ReturnType<typeof fromBinary<typeof WatchUserEventSchema>>
      try {
        len = new DataView(buf.buffer, buf.byteOffset, 4).getUint32(0, false)
        evt = fromBinary(WatchUserEventSchema, buf.slice(4, 4 + len))
      }
      catch (err) {
        clearTimeout(openTimer)
        close()
        reject(err)
        return
      }
      const e = evt.event
      switch (e?.case) {
        case 'initial': {
          const initial = e.value
          epoch = initial.currentEpoch
          for (const [wsId, rec] of Object.entries(initial.workspaces ?? {})) {
            const r = rec.rootNodeId
            if (r)
              setRoot(wsId, r)
          }
          clearTimeout(openTimer)
          resolve()
          break
        }
        case 'batch': {
          // The hub broadcasts each committed op batch as one `batch` event;
          // scan its ops for the seed `CreateWorkspace` that carries the
          // workspace's rootNodeId.
          for (const op of e.value.ops) {
            if (op.body.case === 'createWorkspace')
              setRoot(op.body.value.workspaceId, op.body.value.rootNodeId)
          }
          break
        }
        case 'created': {
          // The `WorkspaceCreated` event carries root_node_id too; treat
          // it as another reliable source for the same datum so a future
          // hub change that retires the raw `CreateWorkspace` op
          // (e.g. moves it to a synthesized "becoming visible" event)
          // doesn't strand the fixture.
          setRoot(e.value.workspaceId, e.value.rootNodeId)
          break
        }
        // This subscription extracts workspace roots and epochs from these event shapes.
      }
    })
  })

  const awaitRootNodeId = (workspaceId: string, timeoutMs = 10_000): Promise<string> => {
    const existing = roots.get(workspaceId)
    if (existing)
      return Promise.resolve(existing)
    if (closed)
      return Promise.reject(new Error(`UserEventsSubscription closed before workspace ${workspaceId} root_node_id arrived`))
    return new Promise((resolve, reject) => {
      let timer: ReturnType<typeof setTimeout>
      const onArrive = (rootNodeId: string) => {
        clearTimeout(timer)
        resolve(rootNodeId)
      }
      timer = setTimeout(() => {
        // Detach the waiter so a late event doesn't fire the rejected promise.
        const arr = waiters.get(workspaceId)
        if (arr) {
          const idx = arr.indexOf(onArrive)
          if (idx >= 0)
            arr.splice(idx, 1)
        }
        reject(new Error(`awaitRootNodeId(${workspaceId}): timed out after ${timeoutMs}ms — the hub did not broadcast CreateWorkspace / WorkspaceCreated to this subscription. Likely an unfixed seed-ops filter-expansion bug.`))
      }, timeoutMs)
      const arr = waiters.get(workspaceId)
      if (arr)
        arr.push(onArrive)
      else
        waiters.set(workspaceId, [onArrive])
    })
  }

  // Detect remote closure (hub stopped, process killed, etc.) so a stale
  // entry in the process-wide cache doesn't get reused. A pending
  // awaitRootNodeId call gets rejected explicitly via the `closed`
  // guard inside that function.
  ws.addEventListener('close', () => {
    closed = true
  })

  return {
    awaitRootNodeId,
    currentEpoch: () => epoch,
    isClosed: () => closed,
    close,
  }
}

// Process-wide cache keyed by (hubUrl, cookie). Each Playwright worker is
// its own Node.js process and gets one subscription per (hub, session) --
// matching how a single browser session holds one long-lived
// `/ws/userevents` for the user. Reusing the subscription
// across `createWorkspaceViaAPI` calls is what makes the test catch a
// regression where the hub fails to deliver seed ops to existing
// subscribers: a fresh-per-call subscription would re-bootstrap from
// the materialized state and silently round the bug.
const userEventsSubs = new Map<string, Promise<UserEventsSubscription>>()

// The cookie IS the identity: `openUserEventsSubscription` authenticates with
// nothing else, so a user id in the key could never distinguish two entries
// the cookie doesn't already distinguish -- it would only cost an extra
// GetCurrentUser round-trip per caller to compute.
function userEventsCacheKey(hubUrl: string, cookie: string): string {
  return `${hubUrl}\x1F${cookie}`
}

/**
 * Return the cached `UserEventsSubscription` for (hubUrl, cookie),
 * opening one if none exists. `createWorkspaceViaAPI` warms this BEFORE
 * dispatching the create RPC — so the subscription is already attached
 * when the hub processes the lifecycle outbox and broadcasts the seed
 * batch.
 */
export function getUserEventsSubscription(
  hubUrl: string,
  cookie: string,
): Promise<UserEventsSubscription> {
  const key = userEventsCacheKey(hubUrl, cookie)
  const existing = userEventsSubs.get(key)
  if (existing) {
    return existing.then((sub) => {
      // A previously cached subscription may have been killed when the
      // hub restarted (full-restart tests). Drop the stale entry and
      // open a fresh subscription so the next workspace creation can
      // observe seed ops on a live socket.
      if (!sub.isClosed())
        return sub
      userEventsSubs.delete(key)
      return getUserEventsSubscription(hubUrl, cookie)
    }, () => {
      // Cached promise rejected — drop it and retry.
      userEventsSubs.delete(key)
      return getUserEventsSubscription(hubUrl, cookie)
    })
  }
  const p = openUserEventsSubscription(hubUrl, cookie).catch((err) => {
    userEventsSubs.delete(key)
    throw err
  })
  userEventsSubs.set(key, p)
  return p
}

/** Close every cached subscription. Called from the global afterAll hook. */
export async function closeAllUserEventsSubscriptions(): Promise<void> {
  const entries = [
    ...userEventsSubs.values(),
  ]
  userEventsSubs.clear()
  for (const p of entries) {
    try {
      const sub = await p
      sub.close()
    }
    catch {
      // best-effort
    }
  }
}
