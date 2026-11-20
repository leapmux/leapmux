import type { HLC, UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { CrdtOp, OpBatch } from '~/generated/proto/leapmux/v1/user_ops_pb'
import type { StorageWrite } from '~/lib/browserStorage'
import { fromBinary, toBinary } from '@bufbuild/protobuf'
import { OpBatchSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { KEY_CRDT_OUTBOX, localStorageDrop, localStorageLoad, localStorageStore } from '~/lib/browserStorage'
import { hlcCmp } from './hlc'

interface StoredCrdtOutbox {
  version: 1
  batches: Uint8Array[]
}

export async function loadCrdtOutbox(): Promise<OpBatch[]> {
  const stored = await localStorageLoad<StoredCrdtOutbox>(KEY_CRDT_OUTBOX)
  if (stored?.version !== 1 || !Array.isArray(stored.batches))
    return []
  const batches: OpBatch[] = []
  for (const bytes of stored.batches) {
    try {
      const batch = fromBinary(OpBatchSchema, bytes)
      if (batch.batchId !== '' && batch.ops.length > 0)
        batches.push(batch)
    }
    catch {
      // A corrupt entry cannot be submitted safely. The next store rewrites
      // the outbox without it.
    }
  }
  return batches
}

export function storeCrdtOutbox(batches: Iterable<OpBatch>): StorageWrite {
  const encoded = [...batches].map(batch => toBinary(OpBatchSchema, batch))
  if (encoded.length === 0)
    return localStorageDrop(KEY_CRDT_OUTBOX)
  return localStorageStore(KEY_CRDT_OUTBOX, {
    version: 1,
    batches: encoded,
  } satisfies StoredCrdtOutbox)
}

/**
 * Whether replaying batch would overwrite canonical state that is at least as
 * new as the batch's original client HLC. One stale operation drops the whole
 * atomic batch; trimming it would violate server completeness rules.
 */
export function outboxBatchIsStale(batch: OpBatch, state: UserCrdtState, currentEpoch: bigint): boolean {
  const previousEpoch = currentEpoch - 1n
  if (batch.epoch <= 0n || (batch.epoch !== currentEpoch && batch.epoch !== previousEpoch))
    return true
  return batch.ops.some(op => opIsStale(op, state))
}

function opIsStale(op: CrdtOp, state: UserCrdtState): boolean {
  const clientHlc = op.clientHlc
  if (!clientHlc)
    return true
  const body = op.body
  switch (body.case) {
    case 'setNodeRegister': {
      const node = state.nodes[body.value.nodeId]
      if (!node)
        return false
      switch (body.value.field.case) {
        case 'parentId':
          return node.parentId !== ''
        case 'kind':
          return registerIsNewer(node.kind?.hlc, clientHlc)
        case 'position':
          return registerIsNewer(node.position?.hlc, clientHlc)
        case 'direction':
          return registerIsNewer(node.direction?.hlc, clientHlc)
        case 'ratios':
          return registerIsNewer(node.ratios?.hlc, clientHlc)
        case 'rows':
          return registerIsNewer(node.rows?.hlc, clientHlc)
        case 'cols':
          return registerIsNewer(node.cols?.hlc, clientHlc)
        case 'rowRatios':
          return registerIsNewer(node.rowRatios?.hlc, clientHlc)
        case 'colRatios':
          return registerIsNewer(node.colRatios?.hlc, clientHlc)
        default:
          return true
      }
    }
    case 'tombstoneNode':
      return registerIsNewer(state.nodes[body.value.nodeId]?.tombstoneAt, clientHlc)
    case 'setTabRegister': {
      const tabId = body.value.tab?.tabId ?? ''
      const tab = state.tabs[tabId]
      if (!tab)
        return false
      switch (body.value.field.case) {
        case 'tileId':
          return registerIsNewer(tab.tileId?.hlc, clientHlc)
        case 'position':
          return registerIsNewer(tab.position?.hlc, clientHlc)
        case 'workerId':
          return registerIsNewer(tab.workerId?.hlc, clientHlc)
        case 'displayMode':
          return registerIsNewer(tab.displayMode?.hlc, clientHlc)
        case 'fileViewMode':
          return registerIsNewer(tab.fileViewMode?.hlc, clientHlc)
        case 'fileDiffBase':
          return registerIsNewer(tab.fileDiffBase?.hlc, clientHlc)
        default:
          return true
      }
    }
    case 'tombstoneTab':
      return registerIsNewer(state.tabs[body.value.tab?.tabId ?? '']?.tombstoneAt, clientHlc)
    case 'reviveTab':
      return registerIsNewer(state.tabs[body.value.tab?.tabId ?? '']?.revivedAt, clientHlc)
    case 'setFloatingWindowRegister': {
      const win = state.floatingWindows[body.value.windowId]
      if (!win)
        return false
      switch (body.value.field.case) {
        case 'rootNodeId':
          return win.rootNodeId !== ''
        case 'workspaceId':
          return registerIsNewer(win.workspaceId?.hlc, clientHlc)
        case 'x':
          return registerIsNewer(win.x?.hlc, clientHlc)
        case 'y':
          return registerIsNewer(win.y?.hlc, clientHlc)
        case 'width':
          return registerIsNewer(win.width?.hlc, clientHlc)
        case 'height':
          return registerIsNewer(win.height?.hlc, clientHlc)
        case 'opacity':
          return registerIsNewer(win.opacity?.hlc, clientHlc)
        default:
          return true
      }
    }
    case 'tombstoneFloatingWindow':
      return registerIsNewer(state.floatingWindows[body.value.windowId]?.tombstoneAt, clientHlc)
    case 'createWorkspace':
    case 'tombstoneWorkspace':
    default:
      // Hub-only or unknown operations must never be restored from a browser
      // outbox.
      return true
  }
}

function registerIsNewer(current: HLC | undefined, submitted: HLC): boolean {
  return current !== undefined && hlcCmp(current, submitted) >= 0
}
