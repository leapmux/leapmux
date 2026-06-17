// ─────────────────────────────────────────────────────────────────────────
// E2E helpers for the OrgCRDT path. Mirrors the frontend's
// useOrgEvents/useOpsSubmitter handshake so tests can seed the CRDT
// after a worker-side `OpenAgent` / `OpenTerminal` fires (those go
// through E2EE channels and don't push into the CRDT themselves —
// the production flow relies on the in-browser `tabStore.addTab` to
// enqueue a SubmitOps batch).
//
// The browser holds ONE long-lived `/ws/orgevents` subscription per
// org session and discovers `workspaces[wsID].rootNodeId` by
// absorbing events on it. The test fixtures mirror that: open the
// subscription BEFORE creating any workspace so the seed
// `SetNodeRegister` + `SetWorkspaceRootNode` ops broadcast through
// the production filter-expansion path. Opening a fresh subscription
// AFTER `createWorkspaceViaAPI` would mask a hub-side bug where the
// seed ops are dropped for existing subscribers — a real production
// regression that the fixture would silently hide.
// ─────────────────────────────────────────────────────────────────────────

import { fromBinary } from '@bufbuild/protobuf'
import { customAlphabet } from 'nanoid'
import { WatchOrgEventSchema } from '../../../src/generated/leapmux/v1/org_ops_pb'
import { authedHeaders } from './api'

const HTTP_TO_WS_RE = /^http/i

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
const nanoid48 = customAlphabet(ALPHABET, 48)

/**
 * OrgEventsSubscription is the test-side analogue of the browser's
 * `useOrgEvents` hook. It opens one long-lived `/ws/orgevents`
 * WebSocket and accumulates the bits the seed-tab flow needs:
 *
 *   - `workspaces[wsID].rootNodeId` — populated either by the
 *     initial `OrgMaterialized` (for workspaces that existed at
 *     subscribe time) or by the raw `SetWorkspaceRootNode` op the
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
export interface OrgEventsSubscription {
  /** Resolves with the workspace's rootNodeId once any inbound event sets it. */
  awaitRootNodeId: (workspaceId: string, timeoutMs?: number) => Promise<string>
  /** Best-effort current epoch (from initial bootstrap, refreshed if a later frame surfaces one). */
  currentEpoch: () => bigint
  /** Whether the underlying WebSocket has closed (locally or remotely). */
  isClosed: () => boolean
  close: () => void
}

/**
 * Open a long-lived `/ws/orgevents?org_id=…` subscription and start
 * accumulating workspace root_node_ids from inbound events. Resolves
 * once the initial `OrgMaterialized` frame lands so callers can rely
 * on a stable bootstrap before triggering further hub state changes.
 */
export async function openOrgEventsSubscription(
  hubUrl: string,
  cookie: string,
  orgId: string,
): Promise<OrgEventsSubscription> {
  const wsUrl = `${hubUrl.replace(HTTP_TO_WS_RE, 'ws')}/ws/orgevents?org_id=${encodeURIComponent(orgId)}`
  const ws = new WebSocket(wsUrl, { headers: { Cookie: cookie } } as any)
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
      reject(new Error('openOrgEventsSubscription: timeout waiting for initial OrgMaterialized'))
    }, 10_000)

    ws.addEventListener('error', () => {
      clearTimeout(openTimer)
      close()
      reject(new Error('openOrgEventsSubscription: WS error'))
    })

    ws.addEventListener('message', (ev) => {
      const buf = ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : null
      if (!buf || buf.length < 4)
        return
      let len: number
      let evt: ReturnType<typeof fromBinary<typeof WatchOrgEventSchema>>
      try {
        len = new DataView(buf.buffer, buf.byteOffset, 4).getUint32(0, false)
        evt = fromBinary(WatchOrgEventSchema, buf.slice(4, 4 + len))
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
          // scan its ops for the seed `SetWorkspaceRootNode` that carries the
          // workspace's rootNodeId.
          for (const op of e.value.ops) {
            if (op.body.case === 'setWorkspaceRootNode')
              setRoot(op.body.value.workspaceId, op.body.value.rootNodeId)
          }
          break
        }
        case 'created': {
          // The `WorkspaceCreated` event carries root_node_id too; treat
          // it as another reliable source for the same datum so a future
          // hub change that retires the raw `SetWorkspaceRootNode` op
          // (e.g. moves it to a synthesized "becoming visible" event)
          // doesn't strand the fixture.
          setRoot(e.value.workspaceId, e.value.rootNodeId)
          break
        }
        // Other event shapes are not interesting to the seed-tab flow.
      }
    })
  })

  const awaitRootNodeId = (workspaceId: string, timeoutMs = 10_000): Promise<string> => {
    const existing = roots.get(workspaceId)
    if (existing)
      return Promise.resolve(existing)
    if (closed)
      return Promise.reject(new Error(`OrgEventsSubscription closed before workspace ${workspaceId} root_node_id arrived`))
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
        reject(new Error(`awaitRootNodeId(${workspaceId}): timed out after ${timeoutMs}ms — the hub did not broadcast SetWorkspaceRootNode / WorkspaceCreated to this subscription. Likely an unfixed seed-ops filter-expansion bug.`))
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

// Process-wide cache keyed by (hubUrl, orgId, cookie). Each Playwright
// worker is its own Node.js process and gets one subscription per
// (hub, org) — matching how a single browser session holds one
// long-lived `/ws/orgevents` for the user. Reusing the subscription
// across `createWorkspaceViaAPI` calls is what makes the test catch a
// regression where the hub fails to deliver seed ops to existing
// subscribers: a fresh-per-call subscription would re-bootstrap from
// the materialized state and silently round the bug.
const orgEventsSubs = new Map<string, Promise<OrgEventsSubscription>>()

function orgEventsCacheKey(hubUrl: string, orgId: string, cookie: string): string {
  return `${hubUrl}\x1F${orgId}\x1F${cookie}`
}

/**
 * Return the cached `OrgEventsSubscription` for (hubUrl, orgId,
 * cookie), opening one if none exists. `createWorkspaceViaAPI` warms
 * this BEFORE dispatching the create RPC — so the subscription is
 * already attached when the hub processes the lifecycle outbox and
 * broadcasts the seed batch.
 */
export function getOrgEventsSubscription(
  hubUrl: string,
  cookie: string,
  orgId: string,
): Promise<OrgEventsSubscription> {
  const key = orgEventsCacheKey(hubUrl, orgId, cookie)
  const existing = orgEventsSubs.get(key)
  if (existing) {
    return existing.then((sub) => {
      // A previously cached subscription may have been killed when the
      // hub restarted (full-restart tests). Drop the stale entry and
      // open a fresh subscription so the next workspace creation can
      // observe seed ops on a live socket.
      if (!sub.isClosed())
        return sub
      orgEventsSubs.delete(key)
      return getOrgEventsSubscription(hubUrl, cookie, orgId)
    }, () => {
      // Cached promise rejected — drop it and retry.
      orgEventsSubs.delete(key)
      return getOrgEventsSubscription(hubUrl, cookie, orgId)
    })
  }
  const p = openOrgEventsSubscription(hubUrl, cookie, orgId).catch((err) => {
    orgEventsSubs.delete(key)
    throw err
  })
  orgEventsSubs.set(key, p)
  return p
}

/** Close every cached subscription. Called from the global afterAll hook. */
export async function closeAllOrgEventsSubscriptions(): Promise<void> {
  const entries = [...orgEventsSubs.values()]
  orgEventsSubs.clear()
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

/**
 * Submit a single op batch (encoded as Connect-RPC JSON) via the hub's
 * `OrgCRDT.SubmitOps` endpoint. Throws on transport failure or a
 * non-committed batch result.
 */
async function submitSetTabRegisterBatch(args: {
  hubUrl: string
  cookie: string
  orgId: string
  epoch: bigint
  tabType: number
  tabId: string
  setRegisters: { tileId?: string, position?: string, workerId?: string }
  originClientId: string
}): Promise<void> {
  const ops: Array<Record<string, unknown>> = []
  const pushOp = (field: Record<string, unknown>) => {
    ops.push({
      orgId: args.orgId,
      opId: nanoid48(),
      originClientId: args.originClientId,
      setTabRegister: {
        tabType: args.tabType,
        tabId: args.tabId,
        ...field,
      },
    })
  }
  if (args.setRegisters.tileId !== undefined)
    pushOp({ tileId: args.setRegisters.tileId })
  if (args.setRegisters.position !== undefined)
    pushOp({ position: args.setRegisters.position })
  if (args.setRegisters.workerId !== undefined)
    pushOp({ workerId: args.setRegisters.workerId })

  const reqBody = {
    orgId: args.orgId,
    epoch: args.epoch.toString(),
    batches: [{
      batchId: nanoid48(),
      ops,
    }],
  }
  const resp = await fetch(`${args.hubUrl}/leapmux.v1.OrgCRDT/SubmitOps`, {
    method: 'POST',
    headers: authedHeaders(args.cookie),
    body: JSON.stringify(reqBody),
  })
  if (!resp.ok) {
    const text = await resp.text().catch(() => '')
    throw new Error(`SubmitOps failed: ${resp.status} ${text}; body=${JSON.stringify(reqBody)}`)
  }
  const data = await resp.json() as { results?: Array<{ rejected?: unknown, committed?: unknown }> }
  if (data.results?.[0]?.rejected) {
    throw new Error(`SubmitOps batch rejected: ${JSON.stringify(data.results[0].rejected)}; body=${JSON.stringify(reqBody)}`)
  }
  if (!data.results?.[0]?.committed) {
    throw new Error(`SubmitOps batch had no committed result: ${JSON.stringify(data)}`)
  }
}

/**
 * Seed an agent / terminal / file tab into the CRDT for tests that
 * created the underlying entity via a direct worker RPC
 * (`openAgentViaAPI` / `openTerminalViaAPI`). Mirrors the production
 * `tabStore.addTab` flow: SetTabRegister(tile_id) + SetTabRegister(
 * position) + SetTabRegister(worker_id).
 *
 * Requires a long-lived `OrgEventsSubscription` opened BEFORE the
 * workspace was created so the hub's seed-ops broadcast is what
 * delivers `rootNodeId`. This is the same path the browser takes —
 * one `/ws/orgevents` per session, populated from `SetWorkspaceRoot
 * Node` ops absorbed in flight. The old behaviour (bootstrap a fresh
 * subscription each call) would mask a hub bug where the seed ops are
 * dropped for existing subscribers; that bug shipped in production
 * once already, so tests must not be allowed to silently route
 * around it.
 */
export async function seedTabIntoWorkspace(args: {
  hubUrl: string
  cookie: string
  orgId: string
  workspaceId: string
  tabType: number
  tabId: string
  workerId: string
  /** Long-lived subscription opened before `createWorkspaceViaAPI`. */
  orgEvents: OrgEventsSubscription
  /** LexoRank position. Defaults to "M" — first slot. */
  position?: string
}): Promise<void> {
  const rootNodeId = await args.orgEvents.awaitRootNodeId(args.workspaceId)
  const epoch = args.orgEvents.currentEpoch()

  await submitSetTabRegisterBatch({
    hubUrl: args.hubUrl,
    cookie: args.cookie,
    orgId: args.orgId,
    epoch,
    tabType: args.tabType,
    tabId: args.tabId,
    setRegisters: {
      tileId: rootNodeId,
      position: args.position ?? 'M',
      workerId: args.workerId,
    },
    originClientId: 'test-fixture',
  })

  // Verify the tab landed in the rendered view via ListTabs (the
  // same path the frontend uses on workspace activation). If the
  // batch committed but ListTabs returns empty, projection-repair
  // dropped the tab or the rendered index wasn't written.
  for (let i = 0; i < 40; i++) {
    const resp = await fetch(`${args.hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
      method: 'POST',
      headers: authedHeaders(args.cookie),
      body: JSON.stringify({ orgId: args.orgId, workspaceIds: [args.workspaceId] }),
    })
    if (resp.ok) {
      const data = await resp.json() as { tabs?: Array<{ tabId: string }> }
      if (data.tabs?.some(t => t.tabId === args.tabId))
        return
    }
    await new Promise(r => setTimeout(r, 50))
  }
  throw new Error(`seedTabIntoWorkspace: tab ${args.tabId} not visible via ListTabs after SubmitOps committed (workspace ${args.workspaceId})`)
}
