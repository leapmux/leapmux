import type { CheckpointDelta } from './checkpointStore'
import type { HlcShape } from './hlc'
import type { PendingOpsManager } from './pendingOps'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeRecordSchema, UserCrdtStateSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { fullCheckpointDelta } from './checkpointChunks'
import { createCheckpointRecorder } from './checkpointRecorder'
import {
  _resetCheckpointStoreForTest,
  appendOpLogSegment,
  readCheckpoint,
  writeCheckpointAndTruncateOpLog,
} from './checkpointStore'
import { hlcClone } from './hlc'
import { pruneTombstonesAtOrBelow } from './pendingOps'

// What the recorder ASKS the store to write is the observation that matters:
// a full rewrite and an incremental one leave byte-identical rows behind, so
// comparing the persisted result cannot tell them apart. The wrapper delegates
// to the real store, so everything else in this file still exercises real IDB.
const storeSpy = vi.hoisted(() => ({ deltas: [] as CheckpointDelta[], appends: 0, ordinals: [] as number[] }))
vi.mock('./checkpointStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./checkpointStore')>()
  return {
    ...actual,
    writeCheckpointAndTruncateOpLog: (
      userId: string,
      clientId: string,
      delta: CheckpointDelta,
      ...rest: [HlcShape, bigint, number?]
    ) => {
      storeSpy.deltas.push(delta)
      return actual.writeCheckpointAndTruncateOpLog(userId, clientId, delta, ...rest)
    },
    // Counted, not stubbed: whether the op-log write HAPPENS is the whole
    // observation for the compaction-skip case, and it is invisible in the
    // persisted result (the rewrite truncates the log either way).
    appendOpLogSegment: (...args: Parameters<typeof actual.appendOpLogSegment>) => {
      storeSpy.appends++
      // The ORDINAL each segment carries, which is the whole observation for a
      // base that lands late: it must continue the adopted sequence, not
      // restart at 0 behind rows the adoption just wrote.
      storeSpy.ordinals.push(args[3])
      return actual.appendOpLogSegment(...args)
    },
  }
})

/** The delta of the LAST rewrite the recorder issued. */
function lastDelta(): CheckpointDelta {
  const delta = storeSpy.deltas.at(-1)
  if (!delta)
    throw new Error('no checkpoint rewrite was issued')
  return delta
}

/** `kind:id` keys of a delta's upserts / deletes, sorted. */
function keysOf(refs: readonly { kind: string, entityId: string }[]): string[] {
  return refs.map(r => `${r.kind}:${r.entityId}`).sort()
}

beforeEach(() => {
  storeSpy.deltas = []
  storeSpy.appends = 0
  storeSpy.ordinals = []
  vi.stubGlobal('indexedDB', new IDBFactory())
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
  _resetCheckpointStoreForTest()
})
afterEach(() => {
  _resetCheckpointStoreForTest()
  vi.unstubAllGlobals()
})

const USER = 'u'
const CLIENT = 'tab-1'
const WM = { physical: 10n, logical: 0n, clientId: 'hub' }

/**
 * A stand-in for PendingOpsManager exposing only what the recorder touches:
 * confirmedState, resumeWatermark, currentEpoch, and the pre-write compaction
 * step. Keeping it this small is itself the point of extracting the recorder --
 * the policy is testable without a Solid root or a real manager.
 *
 * `compactTombstones` calls the PRODUCTION `pruneTombstonesAtOrBelow` rather
 * than stubbing it out, so a change to what the real method prunes shows up
 * here instead of being mocked away.
 */
function fakeMgr(overrides: { watermark?: { physical: bigint, logical: bigint, clientId: string } | undefined } = {}) {
  const state = {
    confirmedState: create(UserCrdtStateSchema, { userId: USER, currentEpoch: 1n }),
    resumeWatermark: 'watermark' in overrides ? overrides.watermark : WM,
    currentEpoch: 1n,
  }
  return {
    state,
    compactTombstones: () => pruneTombstonesAtOrBelow(state.confirmedState, hlcClone(state.resumeWatermark)),
  } as unknown as PendingOpsManager
}

function confirmed(mgr: PendingOpsManager): UserCrdtState {
  return mgr.state.confirmedState
}

function frame(batchId: string) {
  return create(WatchUserEventSchema, { event: { case: 'batch', value: { batchId, ops: [] } } })
}

/** A frame whose single op touches `node:<id>`, so it dirties exactly one chunk. */
function nodeFrame(batchId: string, nodeId: string): WatchUserEvent {
  return create(WatchUserEventSchema, {
    event: {
      case: 'batch',
      value: { batchId, ops: [{ body: { case: 'setNodeRegister', value: { nodeId } } }] },
    },
  } as never)
}

/** Put a node record into confirmedState, as applyOp would. */
function putNode(mgr: PendingOpsManager, nodeId: string, position: string): void {
  confirmed(mgr).nodes[nodeId] = create(NodeRecordSchema, {
    nodeId,
    position: { value: position, hlc: { physical: 1n, logical: 0n, clientId: 'c' } },
  })
}

/** Let the queued microtask flush and its IDB write settle. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++)
    await new Promise(resolve => setTimeout(resolve, 0))
}

/** Seed a checkpoint so reads return `ok` rather than `miss`. */
async function writeBase(state?: UserCrdtState): Promise<void> {
  await writeCheckpointAndTruncateOpLog(
    USER,
    CLIENT,
    fullCheckpointDelta(state ?? create(UserCrdtStateSchema, { userId: USER })),
    { physical: 1n, logical: 0n, clientId: 'hub' },
    1n,
  )
}

/** The owner's persisted chunks as `kind:id -> serialized bytes`, order-free. */
async function persistedChunks(): Promise<Map<string, string>> {
  const read = await readCheckpoint(USER, CLIENT)
  if (read.status !== 'ok')
    throw new Error(`expected a readable checkpoint, got ${read.status}`)
  return new Map(read.chunks.map(c => [`${c.kind}:${c.entityId}`, c.bytes.join(',')]))
}

describe('createCheckpointRecorder', () => {
  it('coalesces a burst of frames into ONE op-log segment', async () => {
    // The append is microtask-coalesced so a drag (~120 frames/sec, half of them
    // self-traffic) costs one IDB transaction per burst rather than one per
    // frame.
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr: fakeMgr() })
    await writeBase()

    recorder.record(frame('a'))
    recorder.record(frame('b'))
    recorder.record(frame('c'))
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toHaveLength(3)
    await recorder.dispose()
  })

  it('rewrites the checkpoint and truncates once the threshold is crossed', async () => {
    await writeBase()
    await appendOpLogSegment(USER, CLIENT, [toBinary(WatchUserEventSchema, frame('stale'))], 0)

    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr: fakeMgr(),
      threshold: 2,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    recorder.record(frame('a'))
    recorder.record(frame('b'))
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    // Compaction ran: the log is empty and the counter is back to zero.
    expect(read.opLogFrames).toEqual([])
    expect(recorder.opLogCount).toBe(0)
    await recorder.dispose()
  })

  it('counts frames already on disk toward the threshold', async () => {
    // Seeding with what hydrate replayed is what drains a log grown by a run of
    // short sessions that each resume via `delta` (no bootstrap, hence no
    // truncate) and append fewer than a threshold's worth of frames.
    await writeBase()
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr: fakeMgr(),
      threshold: 10,
      hydratedFrom: { frames: Array.from({ length: 9 }, (_, i) => frame(`replayed-${i}`)), nextOrdinal: 0 },
    })
    expect(recorder.opLogCount).toBe(9)

    recorder.record(frame('one-more'))
    await settle()

    expect(recorder.opLogCount).toBe(0)
    await recorder.dispose()
  })

  it('backs off instead of re-serializing on every flush when it cannot compact', async () => {
    // With no watermark there is nothing to pin a checkpoint at. The counter
    // stays above the threshold, so without a backoff EVERY subsequent flush
    // re-attempts a serialization that is already known to fail.
    const mgr = fakeMgr({ watermark: undefined })
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, threshold: 1 })

    recorder.record(frame('a'))
    await settle()
    expect(recorder.opLogCount).toBe(1)

    // The next flush must NOT retry -- the retry point moved out by a threshold.
    recorder.record(frame('b'))
    await settle()
    expect(recorder.opLogCount).toBe(2)
    await recorder.dispose()
  })

  it('drops a frame that fails to serialize instead of throwing', async () => {
    // record() runs inside the manager's confirmed-mutation pipeline; a throw
    // would propagate back into the consume* entry points and interrupt their
    // post-record work.
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr: fakeMgr() })
    const bad = { event: { case: 'batch', value: { batchId: 1 as never, ops: [] } } } as never
    expect(() => recorder.record(bad)).not.toThrow()
    await recorder.dispose()
  })

  it('does not throw when the confirmed state fails to serialize', async () => {
    // The sibling of the guard above, on the larger of the two payloads. This
    // one runs synchronously inside bootstrap()'s reset observer, so an
    // unguarded throw escaped through applyMaterialized into the WebSocket
    // message handler -- skipping setBootstrapped and every other
    // post-bootstrap step while the socket stayed healthy and reported nothing.
    const mgr = fakeMgr()
    ;(mgr.state as { confirmedState: unknown }).confirmedState = { not: 'a message' }
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr })
    expect(() => recorder.onCheckpointReset()).not.toThrow()
    await recorder.dispose()
  })

  it('stops recording after dispose', async () => {
    await writeBase()
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr: fakeMgr() })
    await recorder.dispose()

    recorder.record(frame('after-dispose'))
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toEqual([])
  })

  it('dispose() resolves only after the in-flight write settles', async () => {
    // This ordering is what lets the caller clear the account's rows on logout
    // without an append landing after the wipe -- which would leave a previous
    // account's frames on a shared device, in rows nothing ever reads or
    // truncates because their checkpoint is gone.
    await writeBase()
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr: fakeMgr() })
    recorder.record(frame('a'))
    // Let the microtask flush fire (arming the IDB write) but do NOT settle it.
    await Promise.resolve()

    await recorder.dispose()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    // Whatever the append did, it is DONE: no write can now land behind us.
    expect(read.opLogFrames.length).toBeLessThanOrEqual(1)
  })
})

// The point of the sharded checkpoint: a routine rewrite costs the DELTA, not
// the account. The whole-state `toBinary` this replaces ran on the main thread
// every 256 confirmed frames -- mid-drag -- and measured 7 ms at 400 nodes /
// 600 tabs and 57 ms at 2400 / 4800 in this suite's environment.
describe('createCheckpointRecorder incremental rewrites', () => {
  it('rewrites ONLY the chunks its frames touched', async () => {
    const mgr = fakeMgr()
    putNode(mgr, 'untouched', 'original')
    putNode(mgr, 'edited', 'original')
    await writeBase(confirmed(mgr))

    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    putNode(mgr, 'edited', 'changed')
    recorder.record(nodeFrame('b1', 'edited'))
    await settle()

    // The write ASKED for is the observation: a full rewrite would leave
    // byte-identical rows behind, so the persisted result cannot tell the two
    // apart -- only the request can.
    const delta = lastDelta()
    expect(delta.full).toBe(false)
    expect(keysOf(delta.upserts)).toEqual(['node:edited'])
    expect(delta.deletes).toEqual([])
    // ...and the value on disk really did move.
    expect((await persistedChunks()).get('node:untouched')).toBeDefined()
    await recorder.dispose()
  })

  it('does not grow the write as the untouched account grows', async () => {
    // The whole point: the cost of a routine checkpoint tracks the DELTA, not
    // the account. 500 extra nodes must not appear in the upserts.
    const mgr = fakeMgr()
    for (let i = 0; i < 500; i++)
      putNode(mgr, `bulk-${i}`, 'v1')
    await writeBase(confirmed(mgr))

    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    putNode(mgr, 'bulk-3', 'v2')
    recorder.record(nodeFrame('b1', 'bulk-3'))
    await settle()

    expect(lastDelta().upserts).toHaveLength(1)
    await recorder.dispose()
  })

  it('tracks exactly the entities its frames name', async () => {
    const mgr = fakeMgr()
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, hydratedFrom: { frames: [], nextOrdinal: 0 } })

    recorder.record(nodeFrame('b1', 'n1'))
    recorder.record(nodeFrame('b2', 'n2'))
    recorder.record(nodeFrame('b3', 'n1'))

    expect([...recorder.dirtyKeys].sort()).toEqual(['node:n1', 'node:n2'])
    expect(recorder.needsFullRewrite).toBe(false)
    await recorder.dispose()
  })

  it('clears the dirty set only once the write lands', async () => {
    const mgr = fakeMgr()
    await writeBase(confirmed(mgr))
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })

    putNode(mgr, 'n1', 'v1')
    recorder.record(nodeFrame('b1', 'n1'))
    expect(recorder.dirtyKeys.has('node:n1')).toBe(true)
    await settle()
    expect([...recorder.dirtyKeys]).toEqual([])
    await recorder.dispose()
  })

  it('deletes the chunk of an entity that left the state', async () => {
    const mgr = fakeMgr()
    putNode(mgr, 'doomed', 'v1')
    await writeBase(confirmed(mgr))
    expect((await persistedChunks()).has('node:doomed')).toBe(true)

    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    delete confirmed(mgr).nodes.doomed
    recorder.record(nodeFrame('b1', 'doomed'))
    await settle()

    expect(keysOf(lastDelta().deletes)).toEqual(['node:doomed'])
    expect((await persistedChunks()).has('node:doomed')).toBe(false)
    await recorder.dispose()
  })

  it('deletes the chunks the tombstone prune collected, even unnamed by any frame', async () => {
    // The prune drops shells whose tombstoning op may have been recorded MANY
    // checkpoints ago, so the dirty set alone cannot see them. Without the key
    // snapshot bracketing compactTombstones, their chunks survive on disk and
    // the next cold start resurrects every shell the prune just collected.
    const mgr = fakeMgr()
    confirmed(mgr).nodes.tombstoned = create(NodeRecordSchema, {
      nodeId: 'tombstoned',
      tombstoneAt: { physical: 2n, logical: 0n, clientId: 'c' },
    })
    putNode(mgr, 'alive', 'v1')
    await writeBase(confirmed(mgr))
    expect((await persistedChunks()).has('node:tombstoned')).toBe(true)

    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    // A frame about a COMPLETELY DIFFERENT entity triggers the rewrite. The
    // watermark (physical 10) is past the tombstone (physical 2), so
    // compactTombstones collects it inside the rewrite.
    recorder.record(nodeFrame('b1', 'alive'))
    await settle()

    expect(keysOf(lastDelta().deletes)).toEqual(['node:tombstoned'])
    const chunks = await persistedChunks()
    expect(chunks.has('node:tombstoned')).toBe(false)
    expect(chunks.has('node:alive')).toBe(true)
    // The prune-derived key was WRITTEN, so it must be cleared with the rest.
    // Left resident it would re-emit a delete for an already-deleted chunk on
    // every later rewrite, and the set would grow for the session's lifetime.
    expect([...recorder.dirtyKeys]).toEqual([])
    await recorder.dispose()
  })

  it('keeps entities dirtied WHILE a write was in flight', async () => {
    // The write's success handler clears only the keys it actually serialized.
    // Clearing the whole set would strand an entity changed between the
    // serialize and the IDB commit -- its chunk stale on disk, and nothing left
    // to say so.
    const mgr = fakeMgr()
    await writeBase(confirmed(mgr))
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })

    putNode(mgr, 'first', 'v1')
    recorder.record(nodeFrame('b1', 'first'))
    // The flush + rewrite are armed but their IDB write has not settled.
    await Promise.resolve()
    putNode(mgr, 'second', 'v1')
    recorder.record(nodeFrame('b2', 'second'))
    await settle()

    expect(recorder.dirtyKeys.has('node:first')).toBe(false)
    expect(recorder.dirtyKeys.has('node:second')).toBe(true)
    await recorder.dispose()
  })

  it('starts with a FULL rewrite when nothing was hydrated', async () => {
    // A cold start (or a wiped poison record) has no base to be incremental
    // against, so the first rewrite must persist every entity.
    const mgr = fakeMgr()
    putNode(mgr, 'a', 'v1')
    putNode(mgr, 'b', 'v1')
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, threshold: 1 })
    expect(recorder.needsFullRewrite).toBe(true)

    recorder.record(nodeFrame('b1', 'a'))
    await settle()

    expect(lastDelta().full).toBe(true)
    expect([...(await persistedChunks()).keys()].sort()).toEqual(['node:a', 'node:b'])
    expect(recorder.needsFullRewrite).toBe(false)
    await recorder.dispose()
  })

  it('seeds the dirty set from the frames hydration replayed', async () => {
    // Those frames moved the state PAST the persisted chunks, so the entities
    // they name are exactly the chunks that no longer match.
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr: fakeMgr(),
      hydratedFrom: { frames: [nodeFrame('r1', 'replayed-a'), nodeFrame('r2', 'replayed-b')], nextOrdinal: 0 },
    })
    expect([...recorder.dirtyKeys].sort()).toEqual(['node:replayed-a', 'node:replayed-b'])
    expect(recorder.needsFullRewrite).toBe(false)
    await recorder.dispose()
  })

  // A base that is still being WRITTEN (the sibling seed's adoption, which the
  // caller deliberately leaves in flight so its ready gate can open) puts the
  // recorder in a third state: it does not yet know whether the chunks under it
  // are a valid incremental base, and it must not guess in either direction.
  describe('a base that is still in flight', () => {
    /** A promise plus its resolver, so a test can decide when the base lands. */
    function deferredBase() {
      let settle: (b: { frames: WatchUserEvent[], nextOrdinal: number } | undefined) => void = () => {}
      let reject: (err: unknown) => void = () => {}
      const promise = new Promise<{ frames: WatchUserEvent[], nextOrdinal: number } | undefined>((res, rej) => {
        settle = res
        reject = rej
      })
      return { promise, settle, reject }
    }

    it('holds appends until the base lands, then writes them at its ordinal', async () => {
      // The hazard this exists for: appending now would mint ordinal 0 against
      // a log the adoption is about to truncate and re-seed at ordinal 0, and
      // the reader's contiguity check silently truncates at a duplicate.
      const base = deferredBase()
      const mgr = fakeMgr()
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
      })
      putNode(mgr, 'a', 'v1')
      recorder.record(nodeFrame('b1', 'a'))
      await settle()
      expect(storeSpy.appends).toBe(0)

      base.settle({ frames: [], nextOrdinal: 7 })
      await settle()

      // Released, not dropped -- and at the base's ordinal, not at 0.
      expect(storeSpy.appends).toBe(1)
      expect(storeSpy.ordinals).toEqual([7])
      await recorder.dispose()
    })

    it('does not lose a frame recorded while it was holding', async () => {
      // The frames' effects are already in confirmedState and the hub ships
      // only what is strictly after the cursor, so a dropped one never comes
      // back. Its ENTITY must be dirty either way.
      const base = deferredBase()
      const mgr = fakeMgr()
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
      })
      putNode(mgr, 'held', 'v1')
      recorder.record(nodeFrame('b1', 'held'))
      await settle()
      expect([...recorder.dirtyKeys]).toEqual(['node:held'])

      base.settle({ frames: [], nextOrdinal: 0 })
      await settle()
      expect(storeSpy.appends).toBe(1)
      await recorder.dispose()
    })

    it('holds a rewrite until the base lands', async () => {
      // A rewrite would serialize a base the adoption is about to replace
      // wholesale, and its truncate would race the adoption's own.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
        threshold: 1,
      })
      recorder.rewriteNow()
      await settle()
      expect(storeSpy.deltas).toHaveLength(0)

      // DEFERRED, not dropped: the base landing re-issues it with no second
      // rewriteNow() from the caller. hydrateInto's one-shot repair call (fired
      // when the replayed log was truncated or already over the threshold)
      // ALWAYS lands inside this hold on the seed path, so a dropped request
      // meant a seeded tab never performed that repair at all.
      base.settle(undefined)
      await settle()
      expect(storeSpy.deltas).toHaveLength(1)
      await recorder.dispose()
    })

    it('does not re-issue a rewrite nobody asked for while it was holding', async () => {
      // The flip side: the hold must not manufacture a rewrite. Only a request
      // that was actually refused is owed one.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
      })
      base.settle({ frames: [], nextOrdinal: 0 })
      await settle()
      expect(storeSpy.deltas).toHaveLength(0)
      await recorder.dispose()
    })

    it('drops frames it was holding when a bootstrap supersedes the lineage', async () => {
      // The hazard: the pending HOLD parks frames instead of appending them, so
      // unlike every other queue-clearing path they are still sitting there when
      // onCheckpointReset invalidates the lineage. Once the bootstrap's own
      // rewrite lands (needsRebase cleared, log truncated), the NEXT recorded
      // frame would splice those pre-bootstrap bytes onto the fresh log at
      // ordinal 0 -- and the client replays materialized / removed WHOLESALE,
      // with no HLC compare, so the next cold start reinstates discarded records.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
      })
      recorder.record(nodeFrame('pre-bootstrap', 'a'))
      await settle()
      expect(storeSpy.appends).toBe(0)

      recorder.onCheckpointReset()
      await settle()
      base.settle(undefined)
      await settle()

      recorder.record(nodeFrame('post-bootstrap', 'a'))
      await settle()

      const read = await readCheckpoint(USER, CLIENT)
      expect(read.status).toBe('ok')
      if (read.status !== 'ok')
        return
      const batchIds = read.opLogFrames
        .map(b => fromBinary(WatchUserEventSchema, b))
        .map(f => (f.event.case === 'batch' ? f.event.value.batchId : ''))
      expect(batchIds).toEqual(['post-bootstrap'])
      await recorder.dispose()
    })

    it('releases the hold even when it refuses the base', async () => {
      // The refusal path returned before flushAppend, and every held flush had
      // already cleared `appendScheduled` -- so on a tab that went quiet after
      // its reconnect burst nothing was left to revisit the queue at all.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
      })
      recorder.record(nodeFrame('held', 'a'))
      await settle()

      // Invalidate, so the settle below takes adoptBase's refusal arm...
      recorder.onCheckpointReset()
      await settle()
      base.settle({ frames: [], nextOrdinal: 4 })
      await settle()

      // ...and nothing is left queued: no later frame can carry it onto disk.
      recorder.record(nodeFrame('after', 'a'))
      await settle()
      const read = await readCheckpoint(USER, CLIENT)
      expect(read.status).toBe('ok')
      if (read.status !== 'ok')
        return
      expect(read.opLogFrames).toHaveLength(1)
      // The refused base's ordinal was not adopted either.
      expect(storeSpy.ordinals).toEqual([0])
      await recorder.dispose()
    })

    it('reports a FULL rewrite while it is still waiting', async () => {
      // Never "incremental against chunks we have not confirmed": until the
      // adoption commits there may be nothing underneath at all.
      const base = deferredBase()
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr: fakeMgr(),
        hydratedFrom: base.promise,
      })
      expect(recorder.needsFullRewrite).toBe(true)

      base.settle({ frames: [], nextOrdinal: 0 })
      await settle()
      expect(recorder.needsFullRewrite).toBe(false)
      await recorder.dispose()
    })

    it('takes a base that resolves undefined as NO base', async () => {
      // The adoption did not commit, so there are no chunks to be incremental
      // against -- an incremental rewrite would persist a header plus a few
      // shards over nothing.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
        threshold: 1,
      })
      base.settle(undefined)
      await settle()
      expect(recorder.needsFullRewrite).toBe(true)

      recorder.record(nodeFrame('b1', 'a'))
      await settle()
      expect(lastDelta().full).toBe(true)
      await recorder.dispose()
    })

    it('takes a REJECTED base as NO base rather than holding forever', async () => {
      // Staying held would silently stop persisting for the whole session.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
        threshold: 1,
      })
      base.reject(new Error('adopt blew up'))
      await settle()
      expect(recorder.needsFullRewrite).toBe(true)

      recorder.record(nodeFrame('b1', 'a'))
      await settle()
      expect(lastDelta().full).toBe(true)
      await recorder.dispose()
    })

    it('refuses a base that lands after a bootstrap superseded it', async () => {
      // onCheckpointReset invalidates the lineage; the in-flight adoption
      // describes the DISCARDED one, so installing it would re-arm incremental
      // rewrites against chunks the bootstrap already replaced.
      const base = deferredBase()
      const mgr = fakeMgr()
      putNode(mgr, 'a', 'v1')
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr,
        hydratedFrom: base.promise,
        threshold: 1,
      })
      recorder.onCheckpointReset()
      await settle()

      base.settle({ frames: [nodeFrame('r1', 'stale')], nextOrdinal: 9 })
      await settle()

      expect(recorder.dirtyKeys.has('node:stale')).toBe(false)
      // The bootstrap's own FULL rewrite is what re-establishes the base.
      expect(lastDelta().full).toBe(true)
      await recorder.dispose()
    })

    it('does not install a base after dispose', async () => {
      const base = deferredBase()
      const recorder = createCheckpointRecorder({
        userId: USER,
        clientId: CLIENT,
        mgr: fakeMgr(),
        hydratedFrom: base.promise,
      })
      await recorder.dispose()
      base.settle({ frames: [nodeFrame('r1', 'late')], nextOrdinal: 3 })
      await settle()

      expect(recorder.dirtyKeys.has('node:late')).toBe(false)
      expect(storeSpy.appends).toBe(0)
    })
  })

  it('falls back to a FULL rewrite for a structurally malformed frame', async () => {
    // `framedEntityKeys` reads the frame directly, so a frame broken enough
    // (here: a batch whose `ops` is not iterable) makes it THROW rather than
    // return null. record() promises never to throw, and a frame it cannot
    // describe must not leave the dirty set silently short.
    const mgr = fakeMgr({ watermark: undefined })
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, hydratedFrom: { frames: [], nextOrdinal: 0 } })

    expect(() => recorder.record({ event: { case: 'batch', value: { batchId: 'b' } } } as never)).not.toThrow()

    expect(recorder.needsFullRewrite).toBe(true)
    await recorder.dispose()
  })

  it('falls back to a FULL rewrite for a frame shape it cannot describe', async () => {
    // Under-reporting the dirty set is the one unrecoverable direction: a chunk
    // stale on disk but absent from the set is never rewritten again.
    const mgr = fakeMgr()
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, hydratedFrom: { frames: [], nextOrdinal: 0 } })
    expect(recorder.needsFullRewrite).toBe(false)

    recorder.record(create(WatchUserEventSchema, { event: { case: 'presence', value: {} } } as never))

    expect(recorder.needsFullRewrite).toBe(true)
    await recorder.dispose()
  })

  it('re-bases with a FULL rewrite after a bootstrap reset', async () => {
    // A bootstrap replaced confirmedState wholesale, so every chunk of the
    // previous lineage is invalid -- leaving one behind would resurrect a
    // record the fresh snapshot does not contain.
    const mgr = fakeMgr()
    putNode(mgr, 'from-old-lineage', 'v1')
    await writeBase(confirmed(mgr))

    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr, hydratedFrom: { frames: [], nextOrdinal: 0 } })
    // The bootstrap replaces the state, then fires the reset observer.
    ;(mgr.state as { confirmedState: UserCrdtState }).confirmedState
      = create(UserCrdtStateSchema, { userId: USER, currentEpoch: 1n })
    putNode(mgr, 'from-new-lineage', 'v1')
    recorder.onCheckpointReset()
    await settle()

    expect(lastDelta().full).toBe(true)
    expect([...(await persistedChunks()).keys()]).toEqual(['node:from-new-lineage'])
    await recorder.dispose()
  })
})

// A rewrite is best-effort: it early-returns while another write is in flight or
// before a watermark exists, and its IDB write can resolve false. So a recorder
// that has dropped a frame -- or whose lineage a bootstrap replaced -- must not
// resume appending until a rewrite has actually LANDED, or the persisted log
// mixes two lineages (or spans a hole) and its trailing batchEnd frames advance
// the replayed cursor past ops the hub will never re-send.
describe('createCheckpointRecorder write-side bounds', () => {
  // A resume delta carries up to MaxResumeDeltaOps (5000) frames in one burst,
  // ~20x the threshold, so the flush that receives it trips compaction on its
  // own. Appending first writes bytes whose only reader is the truncate that
  // lands microseconds later -- on the reconnect path #267 exists to make cheap.
  it('skips the append when the same flush will trip compaction', async () => {
    const mgr = fakeMgr()
    await writeBase()
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 2,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })

    recorder.record(frame('a'))
    recorder.record(frame('b'))
    await settle()

    // The rewrite ran (it truncates, so the log is empty either way) -- what
    // must NOT have happened is the segment write that preceded it.
    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toEqual([])
    // The persisted result cannot distinguish the two paths -- the rewrite
    // truncates either way -- so assert on the call that must not have been made.
    expect(storeSpy.appends).toBe(0)
    await recorder.dispose()
  })

  // Frames still reach disk when the flush is NOT the one that compacts, so the
  // skip above cannot be mistaken for "appends stopped working".
  it('still appends a flush that does not trip compaction', async () => {
    const mgr = fakeMgr()
    await writeBase()
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 100,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })

    recorder.record(frame('a'))
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toHaveLength(1)
    expect(storeSpy.appends).toBe(1)
    await recorder.dispose()
  })
})

describe('createCheckpointRecorder rebase gating', () => {
  it('stops appending after a dropped frame until a rewrite lands', async () => {
    // No watermark, so every rewriteCheckpoint attempt early-returns and the
    // rebase can never land.
    const mgr = fakeMgr({ watermark: undefined })
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr })
    await writeBase()

    recorder.record(frame('before'))
    await settle()
    const afterFirst = await readCheckpoint(USER, CLIENT)
    if (afterFirst.status !== 'ok')
      throw new Error('expected a readable checkpoint')
    const baseline = afterFirst.opLogFrames.length

    // A frame that cannot be serialized: the oneof value is not a valid message.
    recorder.record({ event: { case: 'batch', value: { batchId: 1 } } } as never)
    await settle()

    recorder.record(frame('after-1'))
    recorder.record(frame('after-2'))
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toHaveLength(baseline)
  })

  it('does not zero the frame counter until the truncate actually lands', () => {
    // onCheckpointReset with no watermark cannot rewrite, so the rows it wanted
    // to truncate are still on disk -- the counter must keep reporting them, or
    // the next compaction attempt is pushed a full threshold away.
    const mgr = fakeMgr({ watermark: undefined })
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      hydratedFrom: { frames: Array.from({ length: 9 }, (_, i) => frame(`f${i}`)), nextOrdinal: 0 },
    })

    recorder.onCheckpointReset()
    expect(recorder.opLogCount).toBe(9)
  })

  it('drops frames already QUEUED when the lineage is invalidated', async () => {
    // record() blocks new appends the moment needsRebase is set, but frames
    // queued in the same turn are flushed one microtask later -- and appending
    // them puts old-lineage rows on disk AFTER the rebase was decided.
    const mgr = fakeMgr({ watermark: undefined })
    await writeBase()
    const recorder = createCheckpointRecorder({ userId: USER, clientId: CLIENT, mgr })

    recorder.record(frame('queued-1'))
    recorder.record(frame('queued-2'))
    // Same synchronous turn: the microtask flush has not run yet.
    recorder.onCheckpointReset()
    await settle()

    const read = await readCheckpoint(USER, CLIENT)
    expect(read.status).toBe('ok')
    if (read.status !== 'ok')
      return
    expect(read.opLogFrames).toEqual([])
    await recorder.dispose()
  })

  it('keeps a frame recorded DURING a rebase write out of neither store', async () => {
    // The rebase rewrite serializes its delta from confirmedState and THEN
    // awaits IDB. A frame arriving in that window is blocked from the op-log
    // (correctly -- the rows are stale), is not in the already-serialized
    // bytes, and its write's success handler clears needsRebase/fullRewrite
    // because `lineage` still matches. Unless the frame is dirty-marked, the
    // entity is left stale on disk with nothing to rewrite it, while the next
    // rewrite pins a watermark PAST its ops -- which the hub never re-ships.
    const mgr = fakeMgr()
    putNode(mgr, 'N', 'P0')
    await writeBase(confirmed(mgr))
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })
    const before = (await persistedChunks()).get('node:N')
    expect(before).toBeDefined()

    // Raises needsRebase and issues a rewrite that serializes P0 synchronously,
    // then awaits IDB. Everything below runs while that write is in flight.
    recorder.onCheckpointReset()
    putNode(mgr, 'N', 'P1')
    recorder.record(nodeFrame('during-rebase', 'N'))
    await settle()

    // Trip one more rewrite so the dirty set is drained to disk.
    putNode(mgr, 'M', 'Q0')
    recorder.record(nodeFrame('after', 'M'))
    await settle()

    expect(keysOf(lastDelta().upserts)).toContain('node:N')
    expect((await persistedChunks()).get('node:N')).not.toBe(before)
    await recorder.dispose()
  })

  it('does not let an in-flight write clear a rebase raised after it serialized', async () => {
    // The write's blob describes the lineage as it was when it serialized. A
    // bootstrap landing while it was in flight makes that blob stale, so
    // clearing needsRebase on its success resumed appending onto a checkpoint
    // of the WRONG lineage -- the precise failure the flag exists to prevent.
    const mgr = fakeMgr()
    await writeBase()
    const recorder = createCheckpointRecorder({
      userId: USER,
      clientId: CLIENT,
      mgr,
      threshold: 1,
      hydratedFrom: { frames: [], nextOrdinal: 0 },
    })

    recorder.record(frame('a'))
    // Arm the flush + rewrite, then invalidate before the IDB write settles.
    await Promise.resolve()
    recorder.onCheckpointReset()
    await settle()

    // The rebase must still be pending, so the next rewrite is FULL.
    expect(recorder.needsFullRewrite).toBe(true)
    await recorder.dispose()
  })
})
