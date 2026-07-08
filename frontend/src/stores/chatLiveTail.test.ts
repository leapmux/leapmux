import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createLiveTailTracker } from '~/stores/chatLiveTail'

/** Run a body with a fresh tracker inside a reactive root (its store needs an owner). */
function withTracker(body: (t: ReturnType<typeof createLiveTailTracker>) => void) {
  createRoot((dispose) => {
    body(createLiveTailTracker())
    dispose()
  })
}

describe('chatlivetail', () => {
  describe('get / bump', () => {
    it('defaults to 0n and raises only on a higher SERVER seq', () => {
      withTracker((t) => {
        expect(t.get('a1')).toBe(0n)
        t.bump('a1', 5n)
        expect(t.get('a1')).toBe(5n)
        t.bump('a1', 3n) // lower -> ignored
        expect(t.get('a1')).toBe(5n)
        t.bump('a1', 0n) // optimistic local -> ignored
        expect(t.get('a1')).toBe(5n)
        t.bump('a1', 9n)
        expect(t.get('a1')).toBe(9n)
      })
    })
  })

  describe('caughtUp', () => {
    it('is true iff the window tail reaches the recorded live tail', () => {
      withTracker((t) => {
        expect(t.caughtUp('a1', 0n)).toBe(true) // nothing observed
        t.bump('a1', 10n)
        expect(t.caughtUp('a1', 9n)).toBe(false)
        expect(t.caughtUp('a1', 10n)).toBe(true)
        expect(t.caughtUp('a1', 11n)).toBe(true)
      })
    })
  })

  describe('settleToWindow', () => {
    it('clamps down to the window tail when the recorded tail did not advance', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        // liveSeqAtEntry = 50 (unchanged since entry) -> clamp to the window tail 30.
        t.settleToWindow('a1', 50n, 30n)
        expect(t.get('a1')).toBe(30n)
      })
    })

    it('skips the clamp when a mid-fetch broadcast advanced the tail past entry', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        // A broadcast during the fetch raised the tail to 60 (> liveSeqAtEntry 50).
        t.bump('a1', 60n)
        t.settleToWindow('a1', 50n, 30n)
        expect(t.get('a1')).toBe(60n) // genuinely-reachable seq preserved
      })
    })

    it('never clamps to an EMPTY window (windowTail 0n) -- a transient empty is not caught up', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        t.settleToWindow('a1', 50n, 0n)
        expect(t.get('a1')).toBe(50n) // left for the authoritative-empty path
      })
    })
  })

  describe('resetToEmptyIfStale', () => {
    it('clamps to 0n on an authoritative empty when the tail did not advance', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        t.resetToEmptyIfStale('a1', 50n)
        expect(t.get('a1')).toBe(0n)
      })
    })

    it('preserves a mid-fetch-raised tail', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        t.bump('a1', 60n) // raised during the fetch
        t.resetToEmptyIfStale('a1', 50n)
        expect(t.get('a1')).toBe(60n)
      })
    })
  })

  describe('onDelete', () => {
    it('drops the recorded tail to the authoritative new tail when the LOADED tail is deleted', () => {
      withTracker((t) => {
        t.bump('a1', 3n) // window caught up to seq 3
        t.onDelete('a1', { removedSeq: 3n, newLatestSeq: 2n, windowTail: 2n })
        expect(t.get('a1')).toBe(2n)
      })
    })

    it('clamps a lagging newLatestSeq at the loaded window tail (delete-vs-insert race)', () => {
      withTracker((t) => {
        t.bump('a1', 3n)
        // The worker's MAX(seq) read lagged: newLatestSeq 1 is below the loaded tail 2.
        t.onDelete('a1', { removedSeq: 3n, newLatestSeq: 1n, windowTail: 2n })
        expect(t.get('a1')).toBe(2n) // never below a row still loaded
      })
    })

    it('sets the authoritative new tail when an UNLOADED beyond-window tail is deleted', () => {
      withTracker((t) => {
        t.bump('a1', 60n) // observed-but-dropped beyond the window (tail loaded = 30)
        t.onDelete('a1', { deletedSeq: 60n, newLatestSeq: 55n, windowTail: 30n })
        expect(t.get('a1')).toBe(55n)
      })
    })

    it('falls back to deletedSeq-1 (clamped at the window tail) for an unloaded delete with no authoritative tail', () => {
      withTracker((t) => {
        t.bump('a1', 60n)
        t.onDelete('a1', { deletedSeq: 60n, windowTail: 30n })
        expect(t.get('a1')).toBe(59n)
      })
    })

    it('lowers to deletedSeq-1 for an unloaded beyond-window delete with an indeterminate (-1) tail', () => {
      withTracker((t) => {
        t.bump('a1', 60n)
        // The worker left new_latest_seq unset (couldn't read the tail), but deletedSeq
        // (60) === the recorded tail, so it is the highest observed seq and the new tail is
        // provably <= 59. Lower to it rather than leaving the recorded tail pointing at the
        // now-deleted 60 (which would keep the "new messages below" affordance falsely lit
        // forever, since 60 can never load again).
        t.onDelete('a1', { deletedSeq: 60n, newLatestSeq: undefined, windowTail: 30n })
        expect(t.get('a1')).toBe(59n)
      })
    })

    it('clears the caught-up gap when the window had loaded right up to the deleted unloaded tail', () => {
      withTracker((t) => {
        t.bump('a1', 31n) // the unloaded tail was 31; the window loaded up to 30
        // 31 is deleted with an indeterminate (unset) tail. deletedSeq-1 = 30 == windowTail,
        // so the reader has now loaded everything that still exists: caughtUp must resolve
        // (recorded clamps to 30), not stay wedged at the deleted 31.
        t.onDelete('a1', { deletedSeq: 31n, newLatestSeq: undefined, windowTail: 30n })
        expect(t.get('a1')).toBe(30n)
        expect(t.caughtUp('a1', 30n)).toBe(true)
      })
    })

    it('uses the loaded window tail for a LOADED-tail delete with an indeterminate (unset) tail', () => {
      withTracker((t) => {
        t.bump('a1', 3n)
        // A loaded-tail delete can see the new last loaded row (windowTail), so an
        // indeterminate broadcast falls back to it rather than a missing tail.
        t.onDelete('a1', { removedSeq: 3n, newLatestSeq: undefined, windowTail: 2n })
        expect(t.get('a1')).toBe(2n)
      })
    })

    it('leaves the recorded tail alone when a NON-tail row is deleted', () => {
      withTracker((t) => {
        t.bump('a1', 10n)
        t.onDelete('a1', { removedSeq: 4n, newLatestSeq: 9n, windowTail: 9n })
        expect(t.get('a1')).toBe(10n)
      })
    })

    it('ignores a deleted optimistic local (seq 0n)', () => {
      withTracker((t) => {
        t.bump('a1', 10n)
        t.onDelete('a1', { removedSeq: 0n, windowTail: 10n })
        expect(t.get('a1')).toBe(10n)
      })
    })
  })

  describe('setAuthoritative', () => {
    it('clamps the recorded tail DOWN to the authoritative seq (over-recorded from a missed delete)', () => {
      withTracker((t) => {
        t.bump('a1', 50n)
        t.setAuthoritative('a1', 30n) // server max is 30; rows 31-50 were deleted while away
        expect(t.get('a1')).toBe(30n)
      })
    })

    it('raises the recorded tail UP to the authoritative seq when the client under-observed', () => {
      withTracker((t) => {
        t.bump('a1', 10n)
        t.setAuthoritative('a1', 40n)
        expect(t.get('a1')).toBe(40n)
      })
    })

    it('clamps a 0n authoritative seq to 0n (the agent is now empty)', () => {
      withTracker((t) => {
        t.bump('a1', 5n)
        t.setAuthoritative('a1', 0n)
        expect(t.get('a1')).toBe(0n)
      })
    })

    it('with a reap ceiling, does NOT lower a tail above it (a live arrival raced in during catch-up)', () => {
      withTracker((t) => {
        t.bump('a1', 50n) // a live broadcast at seq 50 arrived during catch-up
        // CatchUpComplete: latest_seq 30, ceiling (start tail) 40. 50 is ABOVE the
        // ceiling -- a genuine live arrival, not a missed deletion -- so it stays.
        t.setAuthoritative('a1', 30n, 40n)
        expect(t.get('a1')).toBe(50n)
      })
    })

    it('with a reap ceiling, lowers a stale phantom tail inside the (seq, ceiling] band', () => {
      withTracker((t) => {
        t.bump('a1', 38n) // over-recorded from a row deleted during replay
        t.setAuthoritative('a1', 30n, 40n) // 38 is in (30, 40] -> phantom, lower to 30
        expect(t.get('a1')).toBe(30n)
      })
    })
  })

  describe('forget', () => {
    it('drops an agent back to the 0n default and leaves others untouched', () => {
      withTracker((t) => {
        t.bump('a1', 10n)
        t.bump('a2', 20n)
        t.forget('a1')
        expect(t.get('a1')).toBe(0n)
        expect('a1' in t.byAgent).toBe(false)
        expect(t.get('a2')).toBe(20n)
      })
    })

    it('is a no-op for an agent that was never observed', () => {
      withTracker((t) => {
        expect(() => t.forget('ghost')).not.toThrow()
        expect(t.get('ghost')).toBe(0n)
      })
    })
  })
})
