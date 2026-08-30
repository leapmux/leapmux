import type { CheckpointDelta } from './checkpointStore'
import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeRecordSchema, TabRecordSchema, UserCrdtStateSchema } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { WatchUserEventSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { createOpLogAppender } from '~/test-support/opLog'
import { fullCheckpointDelta, serializeHeader } from './checkpointChunks'
import {
  _resetCheckpointStoreForTest,
  adoptCheckpoint,
  appendOpLogSegment,
  MAX_OPLOG_READ_FRAMES,
  readCheckpoint,
  SEED_CANDIDATE_MAX_AGE_MS,
  touchOwner,
  writeCheckpointAndTruncateOpLog,
} from './checkpointStore'
import { loadHydrationState } from './hydrate'

// Partially mocked so ONE case can force the adoption write to fail. Every
// other export -- and adoptCheckpoint itself, unless a test overrides it --
// is the real implementation, so the rest of this file still exercises the
// store rather than a stand-in.
vi.mock('./checkpointStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./checkpointStore')>()
  return { ...actual, adoptCheckpoint: vi.fn(actual.adoptCheckpoint) }
})

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
  it('wipes only the failing tab, leaving the sibling\'s rows intact', async () => {
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

    const before = await readCheckpoint('u', 'tab-good')
    expect(before.status).toBe('ok')

    await loadHydrationState('u', 'tab-bad')

    // Asserted against the STORE, not against a second loadHydrationState call:
    // since the seed path landed, a load for 'tab-good' would also succeed by
    // adopting some other owner, so it can no longer distinguish "this tab's
    // rows survived" from "some rows survived somewhere".
    expect(await readCheckpoint('u', 'tab-good')).toEqual(before)
    // And the poison row really is gone from disk, so the next reload of that
    // tab does not pay another failed parse. (It is not a MISS: having wiped
    // its own row, the tab went on to adopt the sibling's -- see the seeding
    // block below. What matters here is that the undecodable header is gone.)
    const rebuilt = await readCheckpoint('u', 'tab-bad')
    expect(rebuilt.status).toBe('ok')
    expect(rebuilt.status === 'ok' && rebuilt.checkpoint.headerBytes)
      .not
      .toEqual(new Uint8Array([0xFF, 0xFF, 0xFF]))
  })

  // The case above cannot see the wipe: with a sibling present, the adoption's
  // full-replace write leaves 'tab-bad' readable whether or not the wipe ran.
  // With NO sibling to adopt, the wipe is the only thing that can change what is
  // on disk -- so this is the case that actually pins it.
  it('wipes its own corrupt row even when there is no sibling to adopt', async () => {
    await seedDelta('u', 'tab-bad', {
      headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
      upserts: [],
      deletes: [],
      full: true,
    })
    await opLog.append('u', 'tab-bad', [batchFrame('b-bad')])
    expect((await readCheckpoint('u', 'tab-bad')).status).toBe('ok')

    expect(await loadHydrationState('u', 'tab-bad')).toBeNull()

    // GONE, not merely unusable. A wipe that silently did nothing would leave
    // the undecodable header in place, and every later reload would re-pay the
    // failed parse and cold-start again -- forever, and invisibly. That is what
    // the module header's "self-heals" claim rests on.
    expect((await readCheckpoint('u', 'tab-bad')).status).toBe('miss')
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

// Seeding: a tab with nothing usable of its own adopts a SIBLING's pair rather
// than cold-starting on a full projection snapshot. The write clobber that
// forces the per-(user, client) key is a hazard for WRITERS; a reader has none.
describe('loadHydrationState seeding from a sibling', () => {
  it('seeds from the only sibling when this tab has no checkpoint', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])

    const payload = await loadHydrationState('u', 'tab-new')

    expect(payload).not.toBeNull()
    expect(Object.keys(payload!.state.nodes).sort()).toEqual(['n1', 'n2'])
    expect(payload!.frames).toHaveLength(1)
    expect(await payload!.persistedBase).not.toBeNull()
  })

  it('prefers this tab\'s own checkpoint over a fresher sibling', async () => {
    await seed('u', CLIENT, stateOf('u', 5n))
    await seed('u', 'tab-other', stateOf('u', 99n))
    await touchOwner('u', 'tab-other', Date.now() + 1000)

    const payload = await loadHydrationState('u', CLIENT)

    expect(payload!.state.maxHlc!.physical).toBe(5n)
  })

  it('copies the sibling\'s header, chunks and op-log under THIS tab\'s owner key', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1'), batchFrame('b2')])

    await loadHydrationState('u', 'tab-new')

    const mine = await readCheckpoint('u', 'tab-new')
    expect(mine.status).toBe('ok')
    const theirs = await readCheckpoint('u', 'tab-old')
    expect(mine.status === 'ok' && theirs.status === 'ok'
      && mine.checkpoint.headerBytes).toEqual(theirs.status === 'ok' ? theirs.checkpoint.headerBytes : null)
    expect(mine.status === 'ok' && mine.chunks).toHaveLength(theirs.status === 'ok' ? theirs.chunks.length : -1)
    expect(mine.status === 'ok' && mine.opLogFrames).toHaveLength(2)
  })

  it('leaves the sibling\'s rows byte-identical', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])
    const before = await readCheckpoint('u', 'tab-old')

    await loadHydrationState('u', 'tab-new')

    expect(await readCheckpoint('u', 'tab-old')).toEqual(before)
  })

  // The single easiest way to get the adoption wrong. The op-log's `ordinal` is
  // PER OWNER and restarts at 0 after every checkpoint write, so inheriting the
  // source's counter would leave our log starting at N with 0..N-1 absent --
  // which readOpLogFrames reads as a hole and discards an intact log for.
  it('numbers the copied log from 0, not from the sibling\'s next ordinal', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])
    await opLog.append('u', 'tab-old', [batchFrame('b2')])
    await opLog.append('u', 'tab-old', [batchFrame('b3')])
    // The source is three segments in, so a naive copy would hand us 3.
    expect((await readCheckpoint('u', 'tab-old')).status === 'ok'
      && (await readCheckpoint('u', 'tab-old') as { opLogNextOrdinal: number }).opLogNextOrdinal).toBe(3)

    const payload = await loadHydrationState('u', 'tab-new')
    const settled = await payload!.persistedBase
    expect(settled?.nextOpLogOrdinal).toBe(1)

    // And appending at that ordinal really does keep the sequence contiguous:
    // a later cold start reads the whole log, not a prefix ending at a hole.
    // Direct, not via the opLog helper: this test hand-picks the ordinal, which
    // is exactly the case that helper's doc reserves for the raw call.
    await appendOpLogSegment('u', 'tab-new', [batchFrame('b4')], settled!.nextOpLogOrdinal)
    const next = await loadHydrationState('u', 'tab-new')
    expect(next!.frames).toHaveLength(4)
    expect(next!.truncated).toBe(false)
  })

  it('picks the most recently seen sibling when several exist', async () => {
    await seed('u', 'tab-a', stateOf('u', 5n))
    await seed('u', 'tab-b', stateOf('u', 9n))
    // Recency is lastSeenAt, not the checkpoint's watermark: a live tab's
    // watermark is pinned at its last rewrite while its op-log runs on ahead.
    // So tab-a must win despite the LOWER watermark. Age tab-b backwards rather
    // than stamping tab-a into the future -- a future `lastSeenAt` is a clock
    // artefact the scan now skips outright, so the shortcut would test the skip
    // instead of the ranking.
    await touchOwner('u', 'tab-b', Date.now() - 5000)

    const payload = await loadHydrationState('u', 'tab-new')

    expect(payload!.state.maxHlc!.physical).toBe(5n)
  })

  it('skips a corrupt sibling and tries the next one', async () => {
    await seedDelta('u', 'tab-bad', {
      headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
      upserts: [],
      deletes: [],
      full: true,
    })
    await seed('u', 'tab-good', stateOf('u', 5n))
    // Make the CORRUPT one the freshest, so it is tried first.
    await touchOwner('u', 'tab-bad', Date.now() + 5000)

    const payload = await loadHydrationState('u', 'tab-new')

    expect(payload).not.toBeNull()
    expect(Object.keys(payload!.state.nodes).sort()).toEqual(['n1', 'n2'])
  })

  // The safety property the whole seed path rests on. A sibling's recorder
  // believes its chunks are on disk and rewrites INCREMENTALLY against them, so
  // deleting them would leave it writing a header plus a few shards over
  // nothing -- inflicted on a tab whose own records were fine.
  it('never deletes a skipped sibling\'s rows', async () => {
    await seedDelta('u', 'tab-bad', {
      headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
      upserts: [],
      deletes: [],
      full: true,
    })
    const before = await readCheckpoint('u', 'tab-bad')
    expect(before.status).toBe('ok')

    expect(await loadHydrationState('u', 'tab-new')).toBeNull()

    expect(await readCheckpoint('u', 'tab-bad')).toEqual(before)
  })

  it('gives up after the candidate cap rather than reading every owner', async () => {
    for (const id of ['s1', 's2', 's3', 's4']) {
      await seedDelta('u', id, {
        headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
        upserts: [],
        deletes: [],
        full: true,
      })
    }
    // A healthy one, but oldest, so it sits past the cap.
    await seed('u', 'tab-good', stateOf('u', 5n))
    await touchOwner('u', 'tab-good', Date.now() - 1000)

    expect(await loadHydrationState('u', 'tab-new', { limit: 2 })).toBeNull()
  })

  it('returns null when there is no sibling at all', async () => {
    expect(await loadHydrationState('u', 'tab-new')).toBeNull()
  })

  it('never seeds from another user\'s owner', async () => {
    await seed('other-user', 'tab-theirs', stateOf('other-user', 5n))

    expect(await loadHydrationState('u', 'tab-new')).toBeNull()
  })

  it('does not seed from an owner last seen beyond the retention window', async () => {
    await seed('u', 'tab-ancient', stateOf('u', 5n))
    await touchOwner('u', 'tab-ancient', Date.now() - SEED_CANDIDATE_MAX_AGE_MS - 1)

    expect(await loadHydrationState('u', 'tab-new')).toBeNull()
  })

  it('carries a sibling\'s truncated op-log through as truncated', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])
    // A frame that will not decode: the replay stops at it, and the payload is
    // a strict prefix.
    await opLog.append('u', 'tab-old', [new Uint8Array([0xFF, 0xFF, 0xFF])])

    const payload = await loadHydrationState('u', 'tab-new')

    expect(payload!.truncated).toBe(true)
    expect(payload!.frames).toHaveLength(1)
  })

  // ...and copies ONLY that prefix. Adopting the raw list would make this
  // owner's row a second durable copy of the sibling's corruption -- one that
  // every later cold start of THIS tab re-reads and re-truncates at, resuming
  // from a watermark that keeps rewinding.
  it('adopts only the frames it validated, never the sibling\'s poison tail', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])
    await opLog.append('u', 'tab-old', [new Uint8Array([0xFF, 0xFF, 0xFF])])

    const payload = await loadHydrationState('u', 'tab-new')
    expect(await payload!.persistedBase).toEqual({ nextOpLogOrdinal: 1 })

    // Our own row is now self-consistent: what is on disk is exactly what was
    // replayed, so re-reading it is not truncated at all.
    const mine = await readCheckpoint('u', 'tab-new')
    expect(mine.status).toBe('ok')
    if (mine.status !== 'ok')
      return
    expect(mine.opLogFrames).toHaveLength(1)
    expect(mine.opLogTruncated).toBe(false)
    // And the sibling's own row still holds both frames, untouched -- the seed
    // reads it and never writes to it. (The store returns both: it deals in
    // bytes, and only the DECODE in validateCheckpoint can tell them apart.)
    const theirs = await readCheckpoint('u', 'tab-old')
    expect(theirs.status).toBe('ok')
    if (theirs.status === 'ok')
      expect(theirs.opLogFrames).toHaveLength(2)
  })

  // persistedBase is what stops the recorder writing a silently-short
  // checkpoint: an incremental rewrite is only sound over chunks that really
  // are on disk.
  it('reports no persisted base when the adoption write fails, and still returns the state', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))
    await opLog.append('u', 'tab-old', [batchFrame('b1')])
    vi.mocked(adoptCheckpoint).mockResolvedValueOnce(null)

    const payload = await loadHydrationState('u', 'tab-new')

    expect(payload).not.toBeNull()
    expect(Object.keys(payload!.state.nodes).sort()).toEqual(['n1', 'n2'])
    expect(payload!.frames).toHaveLength(1)
    expect(await payload!.persistedBase).toBeNull()
  })

  // useCrdtRuntime's deadline resolves null WITHOUT cancelling the read behind
  // it, so a late adoption would replace a freshly bootstrapped checkpoint with
  // the sibling's older one while the recorder kept appending its own ordinals.
  it('writes nothing when the run was superseded before the adoption', async () => {
    await seed('u', 'tab-old', stateOf('u', 5n))

    const payload = await loadHydrationState('u', 'tab-new', { superseded: () => true })

    expect(await payload!.persistedBase).toBeNull()
    expect((await readCheckpoint('u', 'tab-new')).status).toBe('miss')
  })

  // The WIPE is the other write on this path, and the more destructive one: a
  // superseded run has already cold-started, bootstrapped, and had its recorder
  // write a fresh checkpoint under this same owner key with a `ready` base, so
  // a late wipe deletes those rows while the recorder goes on rewriting
  // INCREMENTALLY against chunks that are no longer there.
  it('does not wipe its own corrupt row when the run was superseded', async () => {
    await seedDelta('u', 'tab-new', {
      headerBytes: new Uint8Array([0xFF, 0xFF, 0xFF]),
      upserts: [],
      deletes: [],
      full: true,
    })
    await seed('u', 'tab-old', stateOf('u', 5n))

    expect(await loadHydrationState('u', 'tab-new', { superseded: () => true })).toBeNull()

    // Still there -- untouched, and not seeded over either.
    const mine = await readCheckpoint('u', 'tab-new')
    expect(mine.status).toBe('ok')
    if (mine.status === 'ok')
      expect([...mine.checkpoint.headerBytes]).toEqual([0xFF, 0xFF, 0xFF])
  })
})
