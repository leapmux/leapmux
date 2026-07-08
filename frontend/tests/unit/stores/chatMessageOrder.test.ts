import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema } from '~/generated/leapmux/v1/agent_pb'
import {
  applyFreshMessage,
  firstServerSeq,
  insertServerBySeq,
  isReapablePhantom,
  lastServerSeq,
  mergeWindow,
  prunableDroppedSpanIds,
  serverMessageEnd,
  serverMessageStart,
  withTrailingLocals,
} from '~/stores/chatMessageOrder'

/** A message with the given id/seq/spanId. seq 0n marks an optimistic local. */
function msg(id: string, seq: bigint, spanId = '') {
  return create(AgentChatMessageSchema, { id, seq, spanId })
}
const ids = (list: { id: string }[]) => list.map(m => m.id)

describe('chatMessageOrder', () => {
  describe('serverMessageEnd', () => {
    it('is 0 for an empty list', () => {
      expect(serverMessageEnd([])).toBe(0)
    })

    it('is the length when every message is a server message', () => {
      expect(serverMessageEnd([msg('a', 1n), msg('b', 2n)])).toBe(2)
    })

    it('excludes trailing optimistic locals', () => {
      expect(serverMessageEnd([msg('a', 1n), msg('local-x', 0n), msg('local-y', 0n)])).toBe(1)
    })

    it('is 0 when every message is a local', () => {
      expect(serverMessageEnd([msg('local-x', 0n), msg('local-y', 0n)])).toBe(0)
    })
  })

  describe('serverMessageStart', () => {
    it('is 0 for an empty list', () => {
      expect(serverMessageStart([])).toBe(0)
    })

    it('is 0 when the first message is a server message', () => {
      expect(serverMessageStart([msg('a', 1n), msg('local-x', 0n)])).toBe(0)
    })

    it('skips leading optimistic locals to the first server message', () => {
      expect(serverMessageStart([msg('local-x', 0n), msg('local-y', 0n), msg('a', 1n)])).toBe(2)
    })

    it('is the length when every message is a local (no server messages)', () => {
      expect(serverMessageStart([msg('local-x', 0n), msg('local-y', 0n)])).toBe(2)
    })
  })

  describe('firstServerSeq', () => {
    it('returns the first server seq, skipping leading optimistic locals', () => {
      expect(firstServerSeq([msg('local-a', 0n), msg('local-b', 0n), msg('m5', 5n), msg('m6', 6n)])).toBe(5n)
    })

    it('returns the head seq when there are no leading locals', () => {
      expect(firstServerSeq([msg('m3', 3n), msg('m4', 4n)])).toBe(3n)
    })

    it('returns undefined (not 0n) for an empty list or an all-locals window', () => {
      // undefined distinguishes "no server head yet" from a genuine head at seq 0.
      expect(firstServerSeq([])).toBeUndefined()
      expect(firstServerSeq([msg('local-a', 0n), msg('local-b', 0n)])).toBeUndefined()
    })
  })

  describe('isReapablePhantom', () => {
    it('keeps rows at or below the authoritative tail', () => {
      expect(isReapablePhantom(5n, 10n)).toBe(false)
      expect(isReapablePhantom(10n, 10n)).toBe(false)
    })
    it('reaps a row beyond the tail when no ceiling is set (CatchUpStart)', () => {
      expect(isReapablePhantom(11n, 10n)).toBe(true)
    })
    it('reaps a row inside the (latestSeq, reapCeilingSeq] band', () => {
      // tail 10, ceiling 20: a row at 15 existed at catch-up start and was deleted
      // during replay -> phantom.
      expect(isReapablePhantom(15n, 10n, 20n)).toBe(true)
      expect(isReapablePhantom(20n, 10n, 20n)).toBe(true) // band is inclusive of the ceiling
    })
    it('exempts a live arrival above the ceiling (raced in during catch-up)', () => {
      // A row at 21 post-dates the catch-up start tail (ceiling 20), so it is a genuine
      // live arrival, not a missed deletion -- never reaped.
      expect(isReapablePhantom(21n, 10n, 20n)).toBe(false)
    })
  })

  describe('lastServerSeq', () => {
    it('returns the last server seq, skipping trailing optimistic locals', () => {
      expect(lastServerSeq([msg('m5', 5n), msg('m6', 6n), msg('local-a', 0n), msg('local-b', 0n)])).toBe(6n)
    })

    it('returns the tail seq when there are no trailing locals', () => {
      expect(lastServerSeq([msg('m3', 3n), msg('m4', 4n)])).toBe(4n)
    })

    it('returns undefined (not 0n) for an empty list or an all-locals window', () => {
      // undefined distinguishes "no server tail yet" from a genuine tail at seq 0.
      expect(lastServerSeq([])).toBeUndefined()
      expect(lastServerSeq([msg('local-a', 0n), msg('local-b', 0n)])).toBeUndefined()
    })
  })

  describe('insertServerBySeq', () => {
    it('inserts into an empty list', () => {
      expect(ids(insertServerBySeq([], msg('a', 5n)))).toEqual(['a'])
    })

    it('appends a newer server message before the trailing locals (fast path)', () => {
      const list = [msg('a', 1n), msg('b', 2n), msg('local-x', 0n)]
      expect(ids(insertServerBySeq(list, msg('c', 3n)))).toEqual(['a', 'b', 'c', 'local-x'])
    })

    it('binary-inserts an out-of-order server message at its seq position', () => {
      const list = [msg('a', 1n), msg('c', 3n), msg('local-x', 0n)]
      expect(ids(insertServerBySeq(list, msg('b', 2n)))).toEqual(['a', 'b', 'c', 'local-x'])
    })

    it('inserts before the first server message when oldest', () => {
      const list = [msg('b', 2n), msg('c', 3n)]
      expect(ids(insertServerBySeq(list, msg('a', 1n)))).toEqual(['a', 'b', 'c'])
    })
  })

  describe('withTrailingLocals', () => {
    it('returns the server array unchanged (same reference) when there are no locals', () => {
      const server = [msg('a', 1n)]
      expect(withTrailingLocals(server, [])).toBe(server)
    })

    it('appends the locals after the server messages', () => {
      const server = [msg('a', 1n)]
      const locals = [msg('local-x', 0n)]
      expect(ids(withTrailingLocals(server, locals))).toEqual(['a', 'local-x'])
    })
  })

  describe('prunableDroppedSpanIds', () => {
    it('returns the spanIds of dropped rows not referenced by any survivor', () => {
      const dropped = [msg('a', 1n, 's1'), msg('b', 2n, 's2')]
      const survivors = [msg('c', 3n, 's3')]
      expect(prunableDroppedSpanIds(dropped, survivors)).toEqual(['s1', 's2'])
    })

    it('spares a dropped span still referenced by a surviving row (split tool pair)', () => {
      // s1's opener was dropped but its result survives -> s1 must NOT be pruned.
      const dropped = [msg('opener', 1n, 's1')]
      const survivors = [msg('result', 2n, 's1')]
      expect(prunableDroppedSpanIds(dropped, survivors)).toEqual([])
    })

    it('dedups a spanId carried by two dropped rows and skips empty spanIds', () => {
      const dropped = [msg('a', 1n, 's1'), msg('b', 2n, ''), msg('c', 3n, 's1')]
      expect(prunableDroppedSpanIds(dropped, [])).toEqual(['s1'])
    })
  })

  describe('applyFreshMessage', () => {
    it('inserts a fresh server message in seq order and reports inserted', () => {
      const prev = [msg('a', 1n), msg('c', 3n)]
      const { next, inserted } = applyFreshMessage(prev, msg('b', 2n), undefined)
      expect(inserted).toBe(true)
      expect(ids(next)).toEqual(['a', 'b', 'c'])
    })

    it('appends an optimistic local (seq 0n) at the tail', () => {
      const prev = [msg('a', 1n)]
      const { next, inserted } = applyFreshMessage(prev, msg('local-x', 0n), undefined)
      expect(inserted).toBe(true)
      expect(ids(next)).toEqual(['a', 'local-x'])
    })

    it('discards a server message whose seq already exists, leaving the array reference unchanged', () => {
      const prev = [msg('a', 1n), msg('b', 2n)]
      const { next, inserted } = applyFreshMessage(prev, msg('b-dup', 2n), undefined)
      expect(inserted).toBe(false)
      expect(next).toBe(prev)
    })

    it('discards a duplicate seq in the MIDDLE of the window (binary-search dedup, not just the tail)', () => {
      // The lower-bound probe must find a non-terminal duplicate, not only the last server row.
      const prev = [msg('a', 1n), msg('b', 2n), msg('c', 3n)]
      const { next, inserted } = applyFreshMessage(prev, msg('b-dup', 2n), undefined)
      expect(inserted).toBe(false)
      expect(next).toBe(prev)
    })

    it('discards a duplicate server seq while optimistic locals trail (dedup bounded to the server region)', () => {
      // The dedup search is bounded to [0, serverEnd); a trailing seq-0n local must not be
      // scanned as a server row nor let the duplicate slip through.
      const prev = [msg('a', 1n), msg('b', 2n), msg('local-x', 0n)]
      const { next, inserted } = applyFreshMessage(prev, msg('b-dup', 2n), undefined)
      expect(inserted).toBe(false)
      expect(next).toBe(prev)
    })

    it('inserts a fresh server message below the window head (lower-bound at index 0, no false dedup)', () => {
      const prev = [msg('b', 2n), msg('c', 3n)]
      const { next, inserted } = applyFreshMessage(prev, msg('a', 1n), undefined)
      expect(inserted).toBe(true)
      expect(ids(next)).toEqual(['a', 'b', 'c'])
    })

    it('drops the reconciled local first, then lands the echo among server messages (not at the local index)', () => {
      // An earlier send (local-1) is still pending when a LATER send (local-2)
      // echoes first: substituting in place would strand local-1 mid-list. The
      // echo (seq 9) must land after the server messages, with local-1 still
      // trailing -- the "optimistic locals always trail" invariant.
      const prev = [msg('a', 1n), msg('local-1', 0n), msg('local-2', 0n)]
      const { next, inserted } = applyFreshMessage(prev, msg('echo', 9n), 'local-2')
      expect(inserted).toBe(true)
      expect(ids(next)).toEqual(['a', 'echo', 'local-1'])
    })

    it('drops the reconciled local even when the echo dedups against an existing server row', () => {
      // The hardest reconcile/dedup intersection: a second identical send (local-2)
      // reconciles to a server echo whose seq ALREADY stands in the window under a
      // different id (the first send's echo, 'first-echo' at seq 9). The duplicate must
      // NOT be re-inserted (inserted:false), but the reconciled local MUST still be
      // dropped -- a fresh array with local-2 gone. Returning `prev` un-dropped here
      // would strand local-2 as a duplicate bubble: the caller gates the span-index and
      // delivery-error writes on `inserted`, but wakes consumers (version bump) on
      // `next !== prev`, so the dropped-but-not-inserted case must change the reference.
      const prev = [msg('a', 1n), msg('first-echo', 9n), msg('local-2', 0n)]
      const { next, inserted } = applyFreshMessage(prev, msg('second-echo', 9n), 'local-2')
      expect(inserted).toBe(false)
      expect(next).not.toBe(prev)
      expect(ids(next)).toEqual(['a', 'first-echo'])
    })
  })

  describe('mergeWindow', () => {
    const noReconciled = new Set<string>()

    it('prepends an older page ahead of the window', () => {
      const prev = [msg('c', 3n), msg('d', 4n)]
      const merged = mergeWindow(prev, [msg('a', 1n), msg('b', 2n)], 'older', noReconciled)
      expect(ids(merged)).toEqual(['a', 'b', 'c', 'd'])
    })

    it('prepends an older page when the window carries trailing locals (head is the first server row)', () => {
      // The window head is the lowest SERVER seq (c@3); the trailing local (seq 0n) is
      // pinned to the tail and must not be mistaken for the head when checking the
      // below-the-head precondition.
      const prev = [msg('c', 3n), msg('d', 4n), msg('local-x', 0n)]
      const merged = mergeWindow(prev, [msg('a', 1n), msg('b', 2n)], 'older', noReconciled)
      expect(ids(merged)).toEqual(['a', 'b', 'c', 'd', 'local-x'])
    })

    it('asserts (throws) in dev/test when an older page overlaps the window head', () => {
      // A fetched "older" row whose seq is NOT below the window head violates the
      // BEFORE-anchored-fetch contract; the blind prepend would leave the window
      // non-ascending and silently break serverMessageEnd / the binary searches. The
      // dev/test assert surfaces the regression loudly instead.
      const prev = [msg('c', 3n), msg('d', 4n)]
      expect(() => mergeWindow(prev, [msg('x', 5n)], 'older', noReconciled)).toThrow(/overlaps the window head/)
    })

    it('falls back to a seq-ordered insert (no throw) when an older page overlaps the head in production', () => {
      // In a production build the assert is compiled out; the safe fallback inserts the
      // off-contract row in seq order rather than prepending it out of place, so the
      // window stays ascending even if the invariant is ever violated.
      vi.stubEnv('DEV', false)
      try {
        const prev = [msg('c', 3n), msg('d', 4n)]
        const merged = mergeWindow(prev, [msg('x', 5n)], 'older', noReconciled)
        expect(ids(merged)).toEqual(['c', 'd', 'x'])
      }
      finally {
        vi.unstubAllEnvs()
      }
    })

    it('splices a newer page in seq order, keeping trailing locals pinned', () => {
      const prev = [msg('a', 1n), msg('local-x', 0n)]
      const merged = mergeWindow(prev, [msg('b', 2n), msg('c', 3n)], 'newer', noReconciled)
      expect(ids(merged)).toEqual(['a', 'b', 'c', 'local-x'])
    })

    it('returns the prev reference unchanged when nothing is new (same id AND seq)', () => {
      const prev = [msg('a', 1n), msg('b', 2n)]
      // The fetched rows match in-window rows by id AND seq -> truly unchanged.
      const merged = mergeWindow(prev, [msg('a', 1n), msg('b', 2n)], 'newer', noReconciled)
      expect(merged).toBe(prev)
    })

    it('replaces a same-id row whose seq changed (a reseq the scrolled-away client missed)', () => {
      // `b` was consolidated to MAX(seq)+1 = 9 under its stable id; the new copy
      // replaces the stale in-window one and lands in seq order.
      const prev = [msg('a', 1n), msg('b', 2n), msg('c', 3n)]
      const merged = mergeWindow(prev, [msg('b', 9n)], 'newer', noReconciled)
      expect(ids(merged)).toEqual(['a', 'c', 'b'])
      expect(merged.find(m => m.id === 'b')?.seq).toBe(9n)
    })

    it('admits a same-id reseq even when its new seq collides with a DIFFERENT in-window row', () => {
      // `b` reseqs to 3, colliding with `c`'s seq. Keyed on id, `b` is still incoming;
      // its stale seq-2 copy is dropped and it lands in order next to `c`.
      const prev = [msg('a', 1n), msg('b', 2n), msg('c', 3n)]
      const merged = mergeWindow(prev, [msg('b', 3n)], 'newer', noReconciled)
      expect(ids(merged).filter(id => id === 'b')).toHaveLength(1)
      expect(merged.find(m => m.id === 'b')?.seq).toBe(3n)
    })

    it('replaces the stale same-seq row when a BRAND-NEW id claims an in-window seq (reseq the client missed)', () => {
      // A fetched row with an unknown id but a seq a different in-window row still
      // holds means a reseq reassigned that seq away from the stale occupant. The
      // newcomer authoritatively owns the seq: the stale 'b' is dropped and the
      // newcomer lands in its place (no data loss, no duplicate slot).
      const prev = [msg('a', 1n), msg('b', 2n)]
      const merged = mergeWindow(prev, [msg('newcomer', 2n)], 'newer', noReconciled)
      expect(ids(merged)).toEqual(['a', 'newcomer'])
      expect(merged.find(m => m.seq === 2n)?.id).toBe('newcomer')
    })

    it('keeps a brand-new id that does NOT collide alongside the existing rows', () => {
      // No seq collision -> the newcomer is simply inserted in order, nothing dropped.
      const prev = [msg('a', 1n), msg('b', 2n)]
      const merged = mergeWindow(prev, [msg('c', 3n)], 'newer', noReconciled)
      expect(ids(merged)).toEqual(['a', 'b', 'c'])
    })

    it('drops a reconciled optimistic local while splicing the newer page', () => {
      const prev = [msg('a', 1n), msg('local-echoed', 0n)]
      const merged = mergeWindow(prev, [msg('server-echo', 2n)], 'newer', new Set(['local-echoed']))
      expect(ids(merged)).toEqual(['a', 'server-echo'])
    })

    it('drops only the reconciled local when the page carries no new rows', () => {
      const prev = [msg('a', 1n), msg('local-echoed', 0n)]
      // The page echoes a row already in the window by id+seq (nothing new), but the
      // local must still be reconciled away.
      const merged = mergeWindow(prev, [msg('a', 1n)], 'newer', new Set(['local-echoed']))
      expect(ids(merged)).toEqual(['a'])
    })
  })
})
