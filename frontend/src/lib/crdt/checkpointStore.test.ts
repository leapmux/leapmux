import type { CheckpointDelta, ChunkRef, ChunkUpsert } from './checkpointStore'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createOpLogAppender } from '~/test-support/opLog'
import {
  _resetCheckpointStoreForTest,
  appendOpLogSegment,
  CHECKPOINT_TTL_MS,
  clearCheckpointAndOpLog,
  clearOwnerCheckpointAndOpLog,
  isCheckpointStoreAvailable,
  MAX_OPLOG_READ_BYTES,
  MAX_OPLOG_READ_FRAMES,
  OWNER_LIVENESS_WINDOW_MS,
  readCheckpoint,
  sweepAbandonedCheckpoints,
  touchOwner,
  writeCheckpointAndTruncateOpLog,
} from './checkpointStore'

const opLog = createOpLogAppender()

// A fresh IndexedDB universe per test, mirroring renderArtifactStore.test.ts.
// The store caches its connection, so _resetCheckpointStoreForTest must run
// alongside the factory swap or it would hold a handle into the old universe.
beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  // fake-indexeddb provides IDBKeyRange alongside IDBFactory; jsdom has neither
  // by default, so stub it for the store's index range queries.
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
  _resetCheckpointStoreForTest()
  opLog.reset()
})
afterEach(() => {
  _resetCheckpointStoreForTest()
  vi.unstubAllGlobals()
})

// This module is a PURE BLOB STORE: `headerBytes`, a chunk's `kind` /
// `entityId` / `bytes`, and an op-log frame are all opaque to it. So these
// cases use plain text rather than proto, which keeps them about the store's
// own contract -- the sharding itself is covered in checkpointChunks.test.ts
// and the parse policy in hydrate.test.ts.
const enc = new TextEncoder()
const dec = new TextDecoder()

function bytes(text: string): Uint8Array {
  return enc.encode(text)
}

function text(b: Uint8Array): string {
  return dec.decode(b)
}

/** A FULL delta carrying `header` plus one chunk per `kind:id -> body` entry. */
function fullDelta(header: string, entities: Record<string, string> = {}): CheckpointDelta {
  return { headerBytes: bytes(header), upserts: chunksOf(entities), deletes: [], full: true }
}

/** An INCREMENTAL delta: only the named chunks move. */
function incrementalDelta(
  header: string,
  entities: Record<string, string> = {},
  deletes: string[] = [],
): CheckpointDelta {
  return {
    headerBytes: bytes(header),
    upserts: chunksOf(entities),
    deletes: deletes.map(refOf),
    full: false,
  }
}

function chunksOf(entities: Record<string, string>): ChunkUpsert[] {
  return Object.entries(entities).map(([key, body]) => ({ ...refOf(key), bytes: bytes(body) }))
}

function refOf(key: string): ChunkRef {
  const [kind, entityId] = key.split(':')
  return { kind: kind!, entityId: entityId! }
}

/** Read and assert the owner has a checkpoint, returning the ok-shaped result. */
async function readOk(userId: string, clientId: string) {
  const read = await readCheckpoint(userId, clientId)
  expect(read.status).toBe('ok')
  if (read.status !== 'ok')
    throw new Error('unreachable')
  return read
}

/** An owner's chunks as a `kind:id -> body` record, order-independent. */
async function chunkMap(userId: string, clientId: string): Promise<Record<string, string>> {
  const read = await readOk(userId, clientId)
  return Object.fromEntries(read.chunks.map(c => [`${c.kind}:${c.entityId}`, text(c.bytes)]))
}

/** Frame byte arrays, comparable with toEqual. */
function asText(frames: Uint8Array[]): string[] {
  return frames.map(text)
}

const WM = { physical: 5n, logical: 0n, clientId: 'c' }

describe('checkpointStore', () => {
  describe('isCheckpointStoreAvailable', () => {
    it('returns true when indexedDB is defined', () => {
      expect(isCheckpointStoreAvailable()).toBe(true)
    })

    it('returns false when indexedDB is undefined (SSR / jsdom without the stub)', () => {
      vi.unstubAllGlobals()
      expect(isCheckpointStoreAvailable()).toBe(false)
    })
  })

  describe('readCheckpoint', () => {
    it('reports a miss when nothing is persisted', async () => {
      expect((await readCheckpoint('user-1', 'tab-1')).status).toBe('miss')
    })

    it('reports a miss when the store is unavailable', async () => {
      vi.unstubAllGlobals()
      expect((await readCheckpoint('user-1', 'tab-1')).status).toBe('miss')
    })
  })

  describe('writeCheckpointAndTruncateOpLog + readCheckpoint round-trip', () => {
    it('persists and reads back the header + watermark + epoch', async () => {
      const ok = await writeCheckpointAndTruncateOpLog(
        'user-1',
        'tab-1',
        fullDelta('header-v1'),
        { physical: 100n, logical: 5n, clientId: 'c1' },
        3n,
      )
      expect(ok).toBe(true)

      const read = await readOk('user-1', 'tab-1')
      // Structured-clone returns a fresh Uint8Array; compare bytes, not refs.
      expect(text(read.checkpoint.headerBytes)).toBe('header-v1')
      expect(read.checkpoint.watermark).toEqual({ physical: 100n, logical: 5n, clientId: 'c1' })
      expect(read.checkpoint.currentEpoch).toBe(3n)
      expect(read.chunks).toEqual([])
      expect(read.opLogFrames).toEqual([])
      expect(read.opLogTruncated).toBe(false)
    })

    it('persists and reads back every entity chunk', async () => {
      await writeCheckpointAndTruncateOpLog(
        'u',
        'c',
        fullDelta('header', { 'node:n1': 'N1', 'tab:t1': 'T1', 'ws:w1': 'W1' }),
        WM,
        1n,
      )
      expect(await chunkMap('u', 'c')).toEqual({ 'node:n1': 'N1', 'tab:t1': 'T1', 'ws:w1': 'W1' })
    })

    it('keeps checkpoints per-user isolated', async () => {
      await writeCheckpointAndTruncateOpLog('user-a', 'tab-1', fullDelta('a'), { physical: 1n, logical: 0n, clientId: 'a' }, 1n)
      await writeCheckpointAndTruncateOpLog('user-b', 'tab-1', fullDelta('b'), { physical: 2n, logical: 0n, clientId: 'b' }, 2n)

      expect((await readOk('user-a', 'tab-1')).checkpoint.watermark.clientId).toBe('a')
      expect((await readOk('user-b', 'tab-1')).checkpoint.watermark.clientId).toBe('b')
      expect((await readCheckpoint('user-c', 'tab-1')).status).toBe('miss')
    })
  })

  // The entire point of the redesign: a routine rewrite must move only the
  // entities that changed, and must leave the rest of the account alone.
  describe('an incremental delta', () => {
    async function seedThreeEntities(): Promise<void> {
      await writeCheckpointAndTruncateOpLog(
        'u',
        'c',
        fullDelta('header@1', { 'node:n1': 'N1', 'node:n2': 'N2', 'tab:t1': 'T1' }),
        WM,
        1n,
      )
    }

    it('rewrites only the named chunks and leaves the others untouched', async () => {
      await seedThreeEntities()
      await writeCheckpointAndTruncateOpLog('u', 'c', incrementalDelta('header@2', { 'node:n2': 'N2-updated' }), WM, 1n)

      expect(await chunkMap('u', 'c')).toEqual({
        'node:n1': 'N1',
        'node:n2': 'N2-updated',
        'tab:t1': 'T1',
      })
      expect(text((await readOk('u', 'c')).checkpoint.headerBytes)).toBe('header@2')
    })

    it('deletes the chunks it names', async () => {
      await seedThreeEntities()
      await writeCheckpointAndTruncateOpLog('u', 'c', incrementalDelta('header@2', {}, ['node:n1', 'tab:t1']), WM, 1n)

      expect(await chunkMap('u', 'c')).toEqual({ 'node:n2': 'N2' })
    })

    it('tolerates a delete for a chunk that is already gone', async () => {
      await seedThreeEntities()
      const ok = await writeCheckpointAndTruncateOpLog('u', 'c', incrementalDelta('h', {}, ['node:nope']), WM, 1n)
      expect(ok).toBe(true)
      expect(Object.keys(await chunkMap('u', 'c'))).toHaveLength(3)
    })

    it('distinguishes the same entity id across kinds', async () => {
      // The chunk key is (owner, KIND, entityId): the three record maps are
      // keyed independently, so an id may repeat across them.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h', { 'node:x': 'as-node', 'tab:x': 'as-tab' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'c', incrementalDelta('h', {}, ['node:x']), WM, 1n)

      expect(await chunkMap('u', 'c')).toEqual({ 'tab:x': 'as-tab' })
    })
  })

  describe('a full delta', () => {
    it('drops chunks the new state no longer has', async () => {
      // A bootstrap replaced confirmedState wholesale, so every chunk of the
      // previous lineage is invalid. Leaving one behind would resurrect a
      // record the snapshot does not contain on the next cold start.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h1', { 'node:old-a': 'A', 'node:old-b': 'B' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h2', { 'node:fresh': 'F' }), WM, 1n)

      expect(await chunkMap('u', 'c')).toEqual({ 'node:fresh': 'F' })
    })

    it('keeps a chunk that the new state still has, with the NEW bytes', async () => {
      // The delete walk and the puts share one transaction, and a cursor sees
      // writes made in its own transaction -- so a put issued while the walk
      // was still advancing would be deleted the moment the cursor reached it.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h1', { 'node:keep': 'OLD', 'node:drop': 'D' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h2', { 'node:keep': 'NEW' }), WM, 1n)

      expect(await chunkMap('u', 'c')).toEqual({ 'node:keep': 'NEW' })
    })

    it('does not disturb another owner\'s chunks', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h', { 'node:a': 'A' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-2', fullDelta('h', { 'node:b': 'B' }), WM, 1n)

      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h2', {}), WM, 1n)

      expect(await chunkMap('u', 'tab-1')).toEqual({})
      expect(await chunkMap('u', 'tab-2')).toEqual({ 'node:b': 'B' })
    })
  })

  // The reason the rows are keyed by (user, client) rather than by user alone.
  describe('per-tab isolation', () => {
    it('gives each client of one user its own checkpoint', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h'), { physical: 10n, logical: 0n, clientId: 'tab-1' }, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-2', fullDelta('h'), { physical: 20n, logical: 0n, clientId: 'tab-2' }, 1n)

      expect((await readOk('u', 'tab-1')).checkpoint.watermark.physical).toBe(10n)
      expect((await readOk('u', 'tab-2')).checkpoint.watermark.physical).toBe(20n)
    })

    it('does not let one tab truncate another tab\'s op-log', async () => {
      // The permanent-data-loss case. A backgrounded tab whose confirmedState
      // lags used to write its stale checkpoint and delete EVERY op-log row for
      // the user, including the segments the foreground tab had appended for
      // later frames. The next cold reload then replayed the stale base plus
      // only the post-truncate frames, and the batch_end frames in that tail
      // advanced the resume cursor straight past the hole -- which the hub never
      // re-sends, because it ships only what is strictly after the cursor.
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h'), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-2', fullDelta('h'), WM, 1n)

      const foreground = [bytes('fg-1'), bytes('fg-2')]
      await opLog.append('u', 'tab-2', foreground)
      expect((await readOk('u', 'tab-2')).opLogFrames.length).toBe(2)

      // The lagging tab compacts. Only its OWN (empty) log may be affected.
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h'), WM, 1n)

      expect(asText((await readOk('u', 'tab-2')).opLogFrames)).toEqual(['fg-1', 'fg-2'])
    })

    it('keeps each tab\'s op-log to itself on read', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h'), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-2', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'tab-1', [bytes('one')])
      await opLog.append('u', 'tab-2', [bytes('two')])

      expect(asText((await readOk('u', 'tab-1')).opLogFrames)).toEqual(['one'])
      expect(asText((await readOk('u', 'tab-2')).opLogFrames)).toEqual(['two'])
    })
  })

  describe('appendOpLogSegment', () => {
    it('writes one segment and reads its frames back in apply order', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [bytes('b1'), bytes('b2'), bytes('b3')])

      expect(asText((await readOk('u', 'c')).opLogFrames)).toEqual(['b1', 'b2', 'b3'])
    })

    it('flattens multiple segments in append order on read', async () => {
      // The generator assigns seq, so this also pins that a store-wide
      // autoIncrement key still orders ONE owner's segments by append time --
      // the index cursor yields equal-index-key records in primary-key order.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [bytes('b1'), bytes('b2')])
      await opLog.append('u', 'c', [bytes('b3')])

      expect(asText((await readOk('u', 'c')).opLogFrames)).toEqual(['b1', 'b2', 'b3'])
    })

    it('interleaves segments from two owners without disturbing either order', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'tab-1', fullDelta('h'), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-2', fullDelta('h'), WM, 1n)

      await opLog.append('u', 'tab-1', [bytes('a1')])
      await opLog.append('u', 'tab-2', [bytes('b1')])
      await opLog.append('u', 'tab-1', [bytes('a2')])
      await opLog.append('u', 'tab-2', [bytes('b2')])

      expect(asText((await readOk('u', 'tab-1')).opLogFrames)).toEqual(['a1', 'a2'])
      expect(asText((await readOk('u', 'tab-2')).opLogFrames)).toEqual(['b1', 'b2'])
    })

    it('a single-frame segment round-trips (the cold-start first-frame case)', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [bytes('solo')])
      expect(asText((await readOk('u', 'c')).opLogFrames)).toEqual(['solo'])
    })

    it('an empty frame list is a no-op', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [])
      expect((await readOk('u', 'c')).opLogFrames).toEqual([])
    })

    it('is best-effort: drops silently when the store is unavailable', async () => {
      vi.unstubAllGlobals()
      await expect(appendOpLogSegment('u', 'c', [new Uint8Array([1, 2, 3])], 0)).resolves.toBeUndefined()
    })
  })

  describe('writeCheckpointAndTruncateOpLog truncates the op-log', () => {
    it('clears the op-log atomically when rewriting the checkpoint', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [bytes('b1'), bytes('b2')])
      expect((await readOk('u', 'c')).opLogFrames.length).toBe(2)

      // Rewrite the checkpoint at a newer watermark — the op-log must clear.
      await writeCheckpointAndTruncateOpLog('u', 'c', incrementalDelta('h2'), { physical: 99n, logical: 0n, clientId: 'c' }, 1n)
      const read = await readOk('u', 'c')
      expect(read.opLogFrames).toEqual([])
      expect(read.checkpoint.watermark.physical).toBe(99n)
    })
  })

  // The op-log is appended on the confirmed hot path (~120 frames/sec during a
  // drag) and drained only by a checkpoint rewrite, which backs off whenever
  // the write fails. So an unbounded read materializes an unbounded log into
  // memory on the very cold start the feature exists to make cheap. The caps
  // are enforced AT the cursor; `truncated` routes to the caller's rewrite,
  // which is what actually drains the log.
  describe('the op-log read caps', () => {
    it('stops at MAX_OPLOG_READ_FRAMES and reports truncated', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', Array.from({ length: MAX_OPLOG_READ_FRAMES + 1 }, () => bytes('f')))

      const read = await readOk('u', 'c')
      expect(read.opLogFrames).toHaveLength(MAX_OPLOG_READ_FRAMES)
      expect(read.opLogTruncated).toBe(true)
    })

    it('does not report truncated at exactly the frame cap', async () => {
      // Boundary: the cap is a ceiling on frames RETURNED, so a log sitting
      // exactly on it is complete and must not trigger a needless rewrite.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', Array.from({ length: MAX_OPLOG_READ_FRAMES }, () => bytes('f')))

      const read = await readOk('u', 'c')
      expect(read.opLogFrames).toHaveLength(MAX_OPLOG_READ_FRAMES)
      expect(read.opLogTruncated).toBe(false)
    })

    it('stops at MAX_OPLOG_READ_BYTES even when far under the frame cap', async () => {
      // A frame cap alone bounds nothing: 100 frames of 64 KiB each is 6 MiB.
      const big = new Uint8Array(64 * 1024)
      const perSegment = 8
      const segments = Math.ceil(MAX_OPLOG_READ_BYTES / (big.byteLength * perSegment)) + 1
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      for (let i = 0; i < segments; i++)
        await opLog.append('u', 'c', Array.from<Uint8Array>({ length: perSegment }).fill(big))

      const read = await readOk('u', 'c')
      expect(read.opLogTruncated).toBe(true)
      expect(read.opLogFrames.length).toBeLessThan(MAX_OPLOG_READ_FRAMES)
      const totalBytes = read.opLogFrames.reduce((sum, f) => sum + f.byteLength, 0)
      expect(totalBytes).toBeLessThanOrEqual(MAX_OPLOG_READ_BYTES)
    })

    it('cuts mid-segment, so the result is still a strict PREFIX', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      // One segment that alone exceeds the frame cap.
      await opLog.append('u', 'c', Array.from({ length: MAX_OPLOG_READ_FRAMES + 10 }, (_, i) => bytes(`f${i}`)))

      const read = await readOk('u', 'c')
      expect(read.opLogFrames).toHaveLength(MAX_OPLOG_READ_FRAMES)
      expect(text(read.opLogFrames[0]!)).toBe('f0')
      expect(text(read.opLogFrames[MAX_OPLOG_READ_FRAMES - 1]!)).toBe(`f${MAX_OPLOG_READ_FRAMES - 1}`)
    })
  })

  // A malformed segment must STOP the read, not be skipped.
  //
  // Skipping it returned frames from BOTH sides of the gap and still reported
  // the read complete. hydrate replays them in order and the `batchEnd` frames
  // among them advance the resume cursor, so the cursor would land PAST the ops
  // in the hole -- which the hub never re-ships, because it sends only what is
  // strictly after the cursor. Reporting a prefix instead routes to the
  // caller's checkpoint rewrite, which drops the bad tail from disk.
  describe('a malformed op-log segment', () => {
    async function seedWithBadSegment(): Promise<void> {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await opLog.append('u', 'c', [bytes('before-1'), bytes('before-2')])
      // A row whose framesBytes is not an array (a partial write, or a quota
      // abort mid-row). Written directly so the store's own guards are bypassed.
      const db = await openDbRaw()
      await new Promise<void>((resolve) => {
        const tx = db.transaction('opLog', 'readwrite')
        tx.objectStore('opLog').add({ userId: 'u', clientId: 'c', ordinal: 1, framesBytes: undefined })
        tx.oncomplete = () => resolve()
      })
      db.close()
      await appendOpLogSegment('u', 'c', [bytes('after')], 2)
    }

    it('returns the PREFIX before it, never frames from past the gap', async () => {
      await seedWithBadSegment()
      expect(asText((await readOk('u', 'c')).opLogFrames)).toEqual(['before-1', 'before-2'])
    })

    it('reports the read as truncated so the caller rewrites the checkpoint', async () => {
      await seedWithBadSegment()
      expect((await readOk('u', 'c')).opLogTruncated).toBe(true)
    })

    // A segment that never landed at all -- the append's write failed and was
    // swallowed, or two tabs sharing one clientId interleaved into one log.
    // Nothing in the ROW is malformed here; the only evidence is the gap in the
    // per-owner ordinal sequence, which is why `seq` (store-wide) cannot see it:
    // another owner's rows legitimately consume seq values, so a gap there means
    // nothing. Without the ordinal, `readOpLogFrames` returned frames from BOTH
    // sides and the post-hole `batchEnd` frames advanced the resume cursor past
    // the missing ops -- which the hub never re-ships, since it sends only what
    // is strictly after the cursor.
    it('stops at a MISSING segment, even though every row is well-formed', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await appendOpLogSegment('u', 'c', [bytes('kept-1')], 0)
      await appendOpLogSegment('u', 'c', [bytes('kept-2')], 1)
      // ordinal 2 never landed.
      await appendOpLogSegment('u', 'c', [bytes('past-the-hole')], 3)

      const read = await readOk('u', 'c')
      expect(asText(read.opLogFrames)).toEqual(['kept-1', 'kept-2'])
      expect(read.opLogTruncated).toBe(true)
    })

    it('reports the ordinal to continue from, so a reload does not restart at 0', async () => {
      // Restarting the sequence behind segments already numbered 0..N-1 would
      // make the NEXT cold start read a well-formed log as holed and discard it.
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await appendOpLogSegment('u', 'c', [bytes('a')], 0)
      await appendOpLogSegment('u', 'c', [bytes('b')], 1)

      const read = await readOk('u', 'c')
      expect(read.opLogNextOrdinal).toBe(2)
      expect(read.opLogTruncated).toBe(false)
    })

    it('restarts the ordinal at 0 after a checkpoint write truncates the log', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h'), WM, 1n)
      await appendOpLogSegment('u', 'c', [bytes('a')], 0)
      await writeCheckpointAndTruncateOpLog('u', 'c', fullDelta('h2'), WM, 1n)

      const read = await readOk('u', 'c')
      expect(read.opLogFrames).toEqual([])
      expect(read.opLogNextOrdinal).toBe(0)
    })

    it('keeps the checkpoint itself usable', async () => {
      // The op-log is a rebuildable tail OVER the checkpoint, so one bad segment
      // must not cost the base -- discarding it would force the full-snapshot
      // projection scan the whole feature exists to avoid.
      await seedWithBadSegment()
      const read = await readOk('u', 'c')
      expect(text(read.checkpoint.headerBytes)).toBe('h')
    })
  })

  describe('clearCheckpointAndOpLog', () => {
    it('removes every client\'s rows for the named user, and only that user', async () => {
      // Logout promises "forget this device's state for this account", so it
      // must span the other tabs' rows too -- otherwise a second tab's
      // checkpoint outlives the logout that was supposed to erase it.
      await writeCheckpointAndTruncateOpLog('user-a', 'tab-1', fullDelta('h', { 'node:a1': 'A1' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('user-a', 'tab-2', fullDelta('h', { 'node:a2': 'A2' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('user-b', 'tab-1', fullDelta('h', { 'node:b1': 'B1' }), { physical: 3n, logical: 0n, clientId: 'b' }, 1n)
      await opLog.append('user-a', 'tab-1', [bytes('b1')])
      await opLog.append('user-a', 'tab-2', [bytes('b2')])
      await opLog.append('user-b', 'tab-1', [bytes('b3')])

      await clearCheckpointAndOpLog('user-a')

      expect((await readCheckpoint('user-a', 'tab-1')).status).toBe('miss')
      expect((await readCheckpoint('user-a', 'tab-2')).status).toBe('miss')
      expect(await countChunkRows()).toBe(1)
      const survivor = await readOk('user-b', 'tab-1')
      expect(survivor.checkpoint.watermark.clientId).toBe('b')
      expect(survivor.opLogFrames.length).toBe(1)
      expect(await chunkMap('user-b', 'tab-1')).toEqual({ 'node:b1': 'B1' })
    })
  })

  describe('clearOwnerCheckpointAndOpLog', () => {
    it('removes one owner\'s metadata, chunks and op-log, and nothing else', async () => {
      await writeCheckpointAndTruncateOpLog('u', 'tab-bad', fullDelta('h', { 'node:x': 'X', 'tab:y': 'Y' }), WM, 1n)
      await writeCheckpointAndTruncateOpLog('u', 'tab-good', fullDelta('h', { 'node:z': 'Z' }), WM, 1n)
      await opLog.append('u', 'tab-bad', [bytes('f')])
      await opLog.append('u', 'tab-good', [bytes('g')])

      await clearOwnerCheckpointAndOpLog('u', 'tab-bad')

      expect((await readCheckpoint('u', 'tab-bad')).status).toBe('miss')
      // Chunks must go with the metadata: an orphaned chunk is unreachable
      // storage that the next full rewrite would have to delete anyway.
      expect(await countChunkRows()).toBe(1)
      expect(await chunkMap('u', 'tab-good')).toEqual({ 'node:z': 'Z' })
      expect(asText((await readOk('u', 'tab-good')).opLogFrames)).toEqual(['g'])
    })
  })
})

// Nothing else ever reclaims an abandoned owner's rows. The client id lives in
// sessionStorage, so it dies with the tab: every closed tab strands a
// (userId, clientId) row holding a serialized UserCrdtState plus its op-log,
// unreachable by any later session and deleted only by a logout many users
// never perform. Left alone the origin's quota is eventually exhausted, writes
// start failing, the checkpoint stops being refreshed, and every refresh falls
// back to the full projection snapshot #267 exists to remove.
describe('sweepAbandonedCheckpoints', () => {
  /** No tab answers the roll-call: every row is a collection candidate. */

  async function seedOwner(userId: string, clientId: string, at: number): Promise<void> {
    await writeCheckpointAndTruncateOpLog(
      userId,
      clientId,
      fullDelta('h', { [`node:${clientId}`]: 'N' }),
      { physical: 1n, logical: 0n, clientId },
      1n,
      at,
    )
    await opLog.append(userId, clientId, [bytes(`f-${clientId}`)])
  }

  it('collects owners past the TTL, with their chunks and op-logs, and never this tab', async () => {
    await seedOwner('u', 'live-tab', 1_000)
    await seedOwner('u', 'dead-tab', 1_000)

    // Far enough past the stamps that both rows are expired -- but `live-tab`
    // is this tab, so it is exempt however stale its last write looks.
    const collected = await sweepAbandonedCheckpoints('u', 'live-tab', {
      now: 1_000 + CHECKPOINT_TTL_MS + 1,
    })

    expect(collected).toBe(1)
    expect((await readCheckpoint('u', 'dead-tab')).status).toBe('miss')
    const survivor = await readOk('u', 'live-tab')
    // The op-log and the chunks went with the metadata row: a segment or a
    // chunk orphaned from its base is unreplayable, and would just accumulate.
    expect(survivor.opLogFrames.length).toBe(1)
    expect(await countChunkRows()).toBe(1)
  })

  it('keeps a fresh owner inside the TTL', async () => {
    await seedOwner('u', 'live-tab', 1_000)
    await seedOwner('u', 'other-tab', 1_000)

    const collected = await sweepAbandonedCheckpoints('u', 'live-tab', {
      now: 1_000 + CHECKPOINT_TTL_MS - 1,
    })
    expect(collected).toBe(0)
    expect((await readCheckpoint('u', 'other-tab')).status).toBe('ok')
  })

  it('caps retained owners oldest-first even when none has expired', async () => {
    // The TTL alone does not bound a user who opens tabs faster than they age
    // out, so the cap is a second, independent bound.
    for (let i = 0; i < 6; i++)
      await seedOwner('u', `tab-${i}`, 1_000 + i)

    // maxOwners counts THIS tab too, so 3 means "me plus two others".
    const collected = await sweepAbandonedCheckpoints('u', 'tab-5', {
      // Far past the liveness window (so none reads as running) but still
      // inside the TTL, which is what isolates the cap arm from the TTL arm.
      now: 1_000 + CHECKPOINT_TTL_MS - 1,
      maxOwners: 3,
    })

    expect(collected).toBe(3)
    expect((await readCheckpoint('u', 'tab-5')).status).toBe('ok')
    // The two youngest others survived; the three oldest went.
    expect((await readCheckpoint('u', 'tab-0')).status).toBe('miss')
    expect((await readCheckpoint('u', 'tab-4')).status).toBe('ok')
  })

  // ALTITUDE-9. The walk visits every row in the global writtenAt index and
  // used to discard any row belonging to another user -- so an account signed
  // out of without an explicit logout left rows NOTHING would ever reclaim.
  // Applying the TTL to them is safe because CHECKPOINT_TTL_MS is comfortably
  // longer than the hub's op-retention window: an expired checkpoint could not
  // delta-resume even if its tab came back.
  describe('other accounts\' rows', () => {
    it('collects a foreign owner that is past the TTL', async () => {
      await seedOwner('u', 'live-tab', 1_000)
      await seedOwner('other-user', 'tab-x', 1_000)

      const collected = await sweepAbandonedCheckpoints('u', 'live-tab', {
        now: 1_000 + CHECKPOINT_TTL_MS + 1,
      })

      expect(collected).toBe(1)
      expect((await readCheckpoint('other-user', 'tab-x')).status).toBe('miss')
      expect(await countChunkRows()).toBe(1)
    })

    it('deletes the FOREIGN owner, not a same-clientId row of this user', async () => {
      // The delete must be keyed off the victim's user id. Keyed off the
      // sweeping user's, an expired foreign row would delete whatever this
      // account happens to have under the same client id -- here, a live tab.
      await seedOwner('u', 'shared-id', 1_000 + CHECKPOINT_TTL_MS)
      await seedOwner('other-user', 'shared-id', 1_000)

      await sweepAbandonedCheckpoints('u', 'my-tab', {
        now: 1_000 + CHECKPOINT_TTL_MS + 1,
      })

      expect((await readCheckpoint('other-user', 'shared-id')).status).toBe('miss')
      expect((await readCheckpoint('u', 'shared-id')).status).toBe('ok')
    })

    it('leaves a FRESH foreign owner alone', async () => {
      await seedOwner('u', 'live-tab', 1_000)
      await seedOwner('other-user', 'tab-x', 1_000)

      const collected = await sweepAbandonedCheckpoints('u', 'live-tab', {
        now: 1_000 + CHECKPOINT_TTL_MS - 1,
      })

      expect(collected).toBe(0)
      expect((await readCheckpoint('other-user', 'tab-x')).status).toBe('ok')
    })

    it('never applies the per-user CAP across accounts', async () => {
      // "This account has too many tabs" says nothing about how many rows
      // another account may keep, so the cap arm stays scoped to `userId`.
      for (let i = 0; i < 6; i++)
        await seedOwner('other-user', `tab-${i}`, 1_000 + i)
      await seedOwner('u', 'my-tab', 1_500)

      const collected = await sweepAbandonedCheckpoints('u', 'my-tab', {
        now: 2_000,
        maxOwners: 2,
      })

      expect(collected).toBe(0)
      expect((await readCheckpoint('other-user', 'tab-0')).status).toBe('ok')
    })
  })

  // SWEEP-2. `writtenAt` moves only on a REWRITE -- once per 256 confirmed
  // frames -- so a quiet but LIVE sibling tab looks arbitrarily stale. The cap
  // arm used to delete its checkpoint and entire op-log out from under it, and
  // that tab then kept appending under an owner key with no base.
  describe('liveness', () => {
    it('reserves a live sibling from the cap arm', async () => {
      for (let i = 0; i < 6; i++)
        await seedOwner('u', `tab-${i}`, 1_000 + i)

      await touchOwner('u', 'tab-0', 1_000 + CHECKPOINT_TTL_MS - 2)
      const collected = await sweepAbandonedCheckpoints('u', 'tab-5', {
        // Far past the liveness window (so none reads as running) but still
        // inside the TTL, which is what isolates the cap arm from the TTL arm.
        now: 1_000 + CHECKPOINT_TTL_MS - 1,
        maxOwners: 3,
      })

      // tab-0 is live, so it is neither collected nor free budget: the cap of
      // 3 covers this tab, tab-0, and one more.
      expect((await readCheckpoint('u', 'tab-0')).status).toBe('ok')
      expect((await readCheckpoint('u', 'tab-4')).status).toBe('ok')
      expect(collected).toBe(3)
    })

    it('reserves a recently-touched owner from the TTL arm too', async () => {
      // A tab open far longer than the TTL without a checkpoint REWRITE is
      // stale-looking and very much alive -- which is exactly why liveness now
      // reads lastSeenAt (refreshed by touchOwner) rather than writtenAt.
      const now = 1_000 + CHECKPOINT_TTL_MS + 1
      await seedOwner('u', 'quiet-but-open', 1_000)
      await seedOwner('u', 'really-dead', 1_000)
      expect(await touchOwner('u', 'quiet-but-open', now - 1)).toBe(true)

      const collected = await sweepAbandonedCheckpoints('u', 'my-tab', { now })

      expect(collected).toBe(1)
      expect((await readCheckpoint('u', 'quiet-but-open')).status).toBe('ok')
      expect((await readCheckpoint('u', 'really-dead')).status).toBe('miss')
    })

    it('reserves a recently-touched FOREIGN owner as well', async () => {
      // Liveness is origin-wide: a running tab signed into another account is
      // still a running tab, and its rows are not ours to collect.
      const now = 1_000 + CHECKPOINT_TTL_MS + 1
      await seedOwner('other-user', 'their-live-tab', 1_000)
      await touchOwner('other-user', 'their-live-tab', now - 1)

      const collected = await sweepAbandonedCheckpoints('u', 'my-tab', { now })

      expect(collected).toBe(0)
      expect((await readCheckpoint('other-user', 'their-live-tab')).status).toBe('ok')
    })

    it('collects an owner that stopped being touched, and never this tab', async () => {
      // Three missed touch intervals is the whole liveness signal; past that an
      // owner is collectable. The sweeping tab is exempt regardless.
      await seedOwner('u', 'my-tab', 1_000)
      await seedOwner('u', 'went-away', 1_000)

      const collected = await sweepAbandonedCheckpoints('u', 'my-tab', {
        now: 1_000 + CHECKPOINT_TTL_MS + 1,
      })

      expect(collected).toBe(1)
      expect((await readCheckpoint('u', 'my-tab')).status).toBe('ok')
      expect((await readCheckpoint('u', 'went-away')).status).toBe('miss')
    })

    it('treats an owner inside the liveness window as running even when the TTL has passed', async () => {
      await seedOwner('u', 'just-touched', 1_000)
      const collected = await sweepAbandonedCheckpoints('u', 'my-tab', {
        now: 1_000 + OWNER_LIVENESS_WINDOW_MS - 1,
        ttlMs: 1,
      })
      expect(collected).toBe(0)
      expect((await readCheckpoint('u', 'just-touched')).status).toBe('ok')
    })
  })

  describe('touchOwner', () => {
    it('moves lastSeenAt without disturbing the checkpoint payload', async () => {
      await seedOwner('u', 'tab', 1_000)
      const before = await readCheckpoint('u', 'tab')
      expect(before.status).toBe('ok')

      expect(await touchOwner('u', 'tab', 9_999)).toBe(true)

      const after = await readCheckpoint('u', 'tab')
      expect(after.status).toBe('ok')
      if (before.status !== 'ok' || after.status !== 'ok')
        return
      expect(after.checkpoint.watermark).toEqual(before.checkpoint.watermark)
      expect(after.checkpoint.currentEpoch).toBe(before.checkpoint.currentEpoch)
      expect(after.checkpoint.headerBytes).toEqual(before.checkpoint.headerBytes)
      expect(after.checkpoint.lastSeenAt).toBe(9_999)
      expect(after.checkpoint.writtenAt).toBe(before.checkpoint.writtenAt)
    })

    it('is a no-op for an owner with no checkpoint, and does not create one', async () => {
      expect(await touchOwner('u', 'never-wrote', 9_999)).toBe(false)
      expect((await readCheckpoint('u', 'never-wrote')).status).toBe('miss')
    })
  })

  it('is a no-op without a user id', async () => {
    await seedOwner('u', 'tab', 1_000)
    expect(await sweepAbandonedCheckpoints('', 'tab', { now: 2_000 })).toBe(0)
    expect((await readCheckpoint('u', 'tab')).status).toBe('ok')
  })
})

/** Open the raw IDB for direct row manipulation / counting in tests. */
async function openDbRaw(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    // NO version argument: a versionless open attaches to whatever version
    // exists, so this neither triggers a spurious version-change transaction
    // against the connection the store holds nor has to be kept in step with
    // DB_VERSION by hand.
    const r = indexedDB.open('leapmux-crdt-state')
    r.onsuccess = () => resolve(r.result)
    r.onerror = () => reject(r.error)
  })
}

/** Total chunk rows across every owner — the orphan check. */
async function countChunkRows(): Promise<number> {
  const db = await openDbRaw()
  const count = await new Promise<number>((resolve, reject) => {
    const req = db.transaction('checkpointChunks', 'readonly').objectStore('checkpointChunks').count()
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  db.close()
  return count
}
