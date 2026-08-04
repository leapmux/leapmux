import type { CheckpointDelta } from './checkpointStore'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeRecordSchema, TabRecordSchema, UserCrdtStateSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { createOpLogAppender } from '~/test-support/opLog'
import { fullCheckpointDelta, serializeHeader } from './checkpointChunks'
import {
  _resetCheckpointStoreForTest,
  MAX_OPLOG_READ_FRAMES,
  writeCheckpointAndTruncateOpLog,
} from './checkpointStore'
import { loadHydrationState } from './hydrate'

const opLog = createOpLogAppender()

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
  _resetCheckpointStoreForTest()
  opLog.reset()
})
afterEach(() => {
  _resetCheckpointStoreForTest()
  vi.unstubAllGlobals()
})

/**
 * Every case here exercises ONE owner: the (user, client) key scoping itself is
 * covered in checkpointStore.test.ts, so binding the client id once keeps these
 * cases about hydration policy.
 */
const CLIENT = 'tab-1'
const WM = { physical: 5n, logical: 0n, clientId: 'c' }

function stateOf(userId: string, maxPhysical = 10n): UserCrdtState {
  return create(UserCrdtStateSchema, {
    userId,
    nodes: { n1: { nodeId: 'n1' }, n2: { nodeId: 'n2' } },
    tabs: { t1: { tabId: 't1', tabType: 1 } },
    floatingWindows: {},
    workspaces: { ws1: { workspaceId: 'ws1', rootNodeId: 'n1' } },
    maxHlc: { physical: maxPhysical, logical: 0n, clientId: 'c' },
    currentEpoch: 1n,
  })
}

function batchFrame(batchId: string): Uint8Array {
  return toBinary(WatchUserEventSchema, create(WatchUserEventSchema, {
    event: { case: 'batch', value: { batchId, ops: [] } },
  }))
}

/** Seed `client`'s checkpoint from a whole state, as a bootstrap rewrite would. */
function seed(userId: string, client: string, state: UserCrdtState, wm = WM, epoch = 1n): Promise<boolean> {
  return writeCheckpointAndTruncateOpLog(userId, client, fullCheckpointDelta(state), wm, epoch)
}

/** Seed with a hand-built delta, for the corruption arms. */
function seedDelta(userId: string, client: string, delta: CheckpointDelta, wm = WM): Promise<boolean> {
  return writeCheckpointAndTruncateOpLog(userId, client, delta, wm, 1n)
}

describe('loadHydrationState', () => {
  it('returns null when no checkpoint exists', async () => {
    expect(await loadHydrationState('user-1', CLIENT)).toBeNull()
  })

  it('returns null when the store is unavailable', async () => {
    vi.unstubAllGlobals()
    expect(await loadHydrationState('user-1', CLIENT)).toBeNull()
  })

  it('returns null for an empty userId', async () => {
    expect(await loadHydrationState('', CLIENT)).toBeNull()
  })

  it('returns null for an empty clientId', async () => {
    expect(await loadHydrationState('u', '')).toBeNull()
  })

  // The corruption wipe is OWNER-scoped. Every failure this module detects
  // belongs to one (userId, clientId) record, but the wipe used to call the
  // user-wide clearCheckpointAndOpLog(userId), which walks byUserId on every
  // store -- so one tab's undecodable blob deleted every other tab's
  // checkpoint AND op-log. Those tabs then cold-started with the full
  // projection snapshot #267 exists to avoid: the exact cross-tab clobber the
  // (user, client) key was introduced to narrow.
  it('wipes only the failing tab, leaving a sibling tab hydratable', async () => {
    // A healthy sibling tab.
    await seed('u', 'tab-good', stateOf('u', 5n))
    await opLog.append('u', 'tab-good', [batchFrame('b-good')])
    // ...and a tab whose header blob does not deserialize.
    await seedDelta('u', 'tab-bad', {
      headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
      upserts: [],
      deletes: [],
      full: true,
    })

    expect(await loadHydrationState('u', 'tab-bad')).toBeNull()

    const survivor = await loadHydrationState('u', 'tab-good')
    expect(survivor).not.toBeNull()
    expect(survivor!.frames).toHaveLength(1)
    expect(Object.keys(survivor!.state.nodes).sort()).toEqual(['n1', 'n2'])
    // And the bad row really is gone, so the next reload of that tab is a miss
    // rather than another failed parse.
    expect(await loadHydrationState('u', 'tab-bad')).toBeNull()
  })

  it('round-trips a well-formed checkpoint + op-log', async () => {
    const source = stateOf('u', 5n)
    await seed('u', CLIENT, source)
    await opLog.append('u', CLIENT, [batchFrame('b1')])
    await opLog.append('u', CLIENT, [batchFrame('b2')])

    const payload = await loadHydrationState('u', CLIENT)
    expect(payload).not.toBeNull()
    expect(payload!.watermark).toEqual(WM)
    expect(payload!.currentEpoch).toBe(1n)
    expect(payload!.frames).toHaveLength(2)
    expect(payload!.frames[0].event.case).toBe('batch')
    expect(payload!.frames[0].event.case === 'batch' && payload!.frames[0].event.value.batchId).toBe('b1')
    expect(payload!.frames[1].event.case === 'batch' && payload!.frames[1].event.value.batchId).toBe('b2')
  })

  // The checkpoint is sharded across a header row plus one chunk per entity, so
  // hydration has to reassemble it. Anything less than EVERY chunk landing is a
  // state silently missing records -- with a resume cursor riding on it.
  describe('re-assembling the sharded state', () => {
    it('restores every entity map from the chunks, byte-identically', async () => {
      const source = stateOf('u', 5n)
      await seed('u', CLIENT, source)

      const payload = await loadHydrationState('u', CLIENT)
      expect(payload!.state).toEqual(source)
    })

    it('restores a state whose maps are all empty', async () => {
      const source = create(UserCrdtStateSchema, { userId: 'u', currentEpoch: 1n })
      await seed('u', CLIENT, source)

      const payload = await loadHydrationState('u', CLIENT)
      expect(payload!.state).toEqual(source)
    })

    it('wipes and returns null when a chunk fails to deserialize', async () => {
      // NOT a partial state: a chunk that cannot be read means the base is
      // short by exactly one record while the header's watermark still claims
      // to describe it. Wiping is the only self-healing answer.
      const source = stateOf('u', 5n)
      await seedDelta('u', CLIENT, {
        headerBytes: serializeHeader(source),
        upserts: [
          { kind: 'node', entityId: 'n1', bytes: toBinary(NodeRecordSchema, source.nodes.n1!) },
          { kind: 'tab', entityId: 't1', bytes: new Uint8Array([0xFF, 0xFE, 0xFD]) },
        ],
        deletes: [],
        full: true,
      })

      expect(await loadHydrationState('u', CLIENT)).toBeNull()
      expect(await loadHydrationState('u', CLIENT)).toBeNull()
    })

    it('wipes and returns null when a chunk names an unknown kind', async () => {
      // A row written by a build that shards something this one does not know.
      const source = stateOf('u', 5n)
      await seedDelta('u', CLIENT, {
        headerBytes: serializeHeader(source),
        upserts: [{ kind: 'gizmo', entityId: 'g1', bytes: toBinary(TabRecordSchema, source.tabs.t1!) }],
        deletes: [],
        full: true,
      })

      expect(await loadHydrationState('u', CLIENT)).toBeNull()
    })

    it('never returns a PARTIAL state — the wipe arm wins over a short one', async () => {
      const source = stateOf('u', 5n)
      await seedDelta('u', CLIENT, {
        headerBytes: serializeHeader(source),
        upserts: [
          { kind: 'node', entityId: 'n1', bytes: toBinary(NodeRecordSchema, source.nodes.n1!) },
          { kind: 'node', entityId: 'n2', bytes: new Uint8Array([0x08, 0xFF]) },
        ],
        deletes: [],
        full: true,
      })

      const payload = await loadHydrationState('u', CLIENT)
      // Never a state carrying n1 alone.
      expect(payload).toBeNull()
    })
  })

  it('returns a payload with empty frames when the checkpoint has no op-log', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    const payload = await loadHydrationState('u', CLIENT)
    expect(payload).not.toBeNull()
    expect(payload!.frames).toEqual([])
  })

  it('wipes both stores and returns null when the checkpoint header is unparseable', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, [batchFrame('b1')])
    // Now corrupt the header in place via a direct rewrite.
    await seedDelta('u', CLIENT, {
      headerBytes: new Uint8Array([0xFF, 0xFE, 0xFD]),
      upserts: [],
      deletes: [],
      full: true,
    })
    await opLog.append('u', CLIENT, [batchFrame('b2')])

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
    // The pair was wiped: a second load also returns null (no poison record).
    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })

  it('keeps the checkpoint and replays the decodable prefix when an op-log frame is unparseable', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    // One good frame, then a corrupt frame, then another good one that must NOT
    // be replayed (it sits after the break, so its ordering context is lost).
    await opLog.append('u', CLIENT, [batchFrame('good')])
    await opLog.append('u', CLIENT, [new Uint8Array([0xFF, 0xFE, 0xFD])])
    await opLog.append('u', CLIENT, [batchFrame('after-corrupt')])

    const payload = await loadHydrationState('u', CLIENT)
    // The checkpoint is independently valid, so it survives: discarding it
    // would force the full-snapshot projection scan #267 exists to avoid.
    expect(payload).not.toBeNull()
    expect(payload!.truncated).toBe(true)
    expect(payload!.frames).toHaveLength(1)
    expect(payload!.frames[0].event.case).toBe('batch')
    // The checkpoint itself is untouched, so a second load still finds it.
    expect(await loadHydrationState('u', CLIENT)).not.toBeNull()
  })

  it('reports truncated=false when every op-log frame decodes', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, [batchFrame('a'), batchFrame('b')])

    const payload = await loadHydrationState('u', CLIENT)
    expect(payload).not.toBeNull()
    expect(payload!.truncated).toBe(false)
    expect(payload!.frames).toHaveLength(2)
  })

  it('wipes both stores and returns null when the checkpoint names another tenant', async () => {
    // A blob keyed under 'u' but carrying another user's state must be refused
    // the same way the wire path refuses a foreign-tenant UserMaterialized --
    // otherwise the persisted record, not the session, decides what is rendered.
    await seed('u', CLIENT, stateOf('other-user', 5n))
    await opLog.append('u', CLIENT, [batchFrame('b1')])

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
    // Wiped, so the poison record cannot be re-adopted on the next reload.
    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })

  it('wipes both stores and returns null when the checkpoint names NO tenant', async () => {
    // The one blob whose provenance is completely unknown. Guarding the tenant
    // check with `state.userId && …` short-circuits past exactly this record,
    // adopting it as the session's own state -- while every other validation
    // here (watermark, epoch, chunk kind) treats absent as fatal. No writer
    // produces one, which is precisely why a row that has one is corrupt.
    await seed('u', CLIENT, stateOf('', 5n))
    await opLog.append('u', CLIENT, [batchFrame('b1')])

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })

  // The epoch two lines below this check has always been range-checked; the
  // watermark was type-checked only, even though the surrounding comments (and
  // the module header) promise "in-range" for both. An out-of-range physical
  // survives hydration and then fails `validateResumeHlc` at every socket open,
  // so the client silently full-snapshots on every connect until some bootstrap
  // happens to overwrite it.
  it('wipes when the persisted watermark is out of int64 range', async () => {
    await seed('u', CLIENT, stateOf('u', 5n), { physical: 2n ** 63n, logical: 0n, clientId: 'c' })

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })

  it('carries the store\'s over-cap truncation through to the caller', async () => {
    // The frame and byte ceilings are enforced at the store's CURSOR, not here:
    // a bound checked after the whole log has been flattened into memory has
    // already paid the cost it exists to refuse. What hydrate owes is passing
    // `truncated` on, since that is what makes the caller rewrite the
    // checkpoint -- the only thing that actually drains the log.
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, Array.from({ length: MAX_OPLOG_READ_FRAMES + 1 }, (_, i) => batchFrame(`b${i}`)))

    const payload = await loadHydrationState('u', CLIENT)
    expect(payload).not.toBeNull()
    expect(payload!.frames).toHaveLength(MAX_OPLOG_READ_FRAMES)
    expect(payload!.truncated).toBe(true)
  })

  it('does not report truncated at exactly the cap', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, Array.from({ length: MAX_OPLOG_READ_FRAMES }, (_, i) => batchFrame(`b${i}`)))

    const payload = await loadHydrationState('u', CLIENT)
    expect(payload!.frames).toHaveLength(MAX_OPLOG_READ_FRAMES)
    expect(payload!.truncated).toBe(false)
  })

  it('wipes both stores and returns null when the watermark is missing', async () => {
    // Seed a valid checkpoint + op-log, then overwrite the metadata row with a
    // malformed watermark via a direct IDB put (bypassing the validator).
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, [batchFrame('b1')])
    const db = await openDbRaw()
    await new Promise<void>((resolve) => {
      const tx = db.transaction('checkpoints', 'readwrite')
      tx.objectStore('checkpoints').put({
        userId: 'u',
        clientId: CLIENT,
        headerBytes: serializeHeader(stateOf('u', 5n)),
        watermark: { physical: 5n, logical: 0n, clientId: 123 as never },
        currentEpoch: 1n,
        writtenAt: 0,
      })
      tx.oncomplete = () => resolve()
    })
    db.close()

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })

  it('wipes both stores and returns null when the epoch is out of range', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    await opLog.append('u', CLIENT, [batchFrame('b1')])
    // Overwrite with an out-of-int64 epoch.
    const db = await openDbRaw()
    await new Promise<void>((resolve) => {
      const tx = db.transaction('checkpoints', 'readwrite')
      tx.objectStore('checkpoints').put({
        userId: 'u',
        clientId: CLIENT,
        headerBytes: serializeHeader(stateOf('u', 5n)),
        watermark: { physical: 5n, logical: 0n, clientId: 'c' },
        currentEpoch: 2n ** 63n, // out of int64 range
        writtenAt: 0,
      })
      tx.oncomplete = () => resolve()
    })
    db.close()

    expect(await loadHydrationState('u', CLIENT)).toBeNull()
  })
})

/** Open the raw IDB for direct row manipulation in tests. */
async function openDbRaw(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    // NO version argument: a versionless open attaches to whatever version
    // exists, so this neither triggers a spurious version-change transaction
    // against the connection the store holds nor has to be kept in step with
    // DB_VERSION by hand (pinning it meant every bump silently broke these
    // cases with a VersionError). The store's own open, which the callers above
    // have already performed, is what creates the schema.
    const r = indexedDB.open('leapmux-crdt-state')
    r.onsuccess = () => resolve(r.result)
    r.onerror = () => reject(r.error)
  })
}
