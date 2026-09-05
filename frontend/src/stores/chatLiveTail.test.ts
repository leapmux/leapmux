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
    it('defaults to 0n and raises only on a higher positive sequence', () => {
      withTracker((t) => {
        expect(t.get('a1')).toBe(0n)
        t.bump('a1', 5n)
        expect(t.get('a1')).toBe(5n)
        t.bump('a1', 3n) // lower -> ignored
        expect(t.get('a1')).toBe(5n)
        t.bump('a1', 0n)
        t.bump('a1', -1n)
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
