import type { RailRangeInputs } from '~/stores/chatMessageMarks'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { createMessageMarksStore, insertMarkSorted, removeMarkAt, resolveRailRange } from '~/stores/chatMessageMarks'

/** Run a body with a fresh store inside a reactive root (its store needs an owner). */
function withStore(body: (s: ReturnType<typeof createMessageMarksStore>) => void) {
  createRoot((dispose) => {
    body(createMessageMarksStore())
    dispose()
  })
}

const U = MarkType.USER_MESSAGE
const C = MarkType.CONTROL_RESPONSE

describe('chatmessagemarks', () => {
  describe('pure helpers', () => {
    it('insertMarkSorted keeps ascending order and is idempotent on a duplicate seq', () => {
      let marks = insertMarkSorted([], 5n, U)
      marks = insertMarkSorted(marks, 2n, U)
      marks = insertMarkSorted(marks, 8n, C)
      marks = insertMarkSorted(marks, 5n, U) // duplicate seq
      expect(marks.map(m => m.seq)).toEqual([2n, 5n, 8n])
      expect(marks.map(m => m.type)).toEqual([U, U, C])
    })

    it('insertMarkSorted returns the SAME reference when the seq is already present', () => {
      const marks = insertMarkSorted([], 3n, U)
      expect(insertMarkSorted(marks, 3n, U)).toBe(marks)
    })

    it('removeMarkAt drops the seq and returns the same reference when absent', () => {
      const marks = insertMarkSorted(insertMarkSorted([], 1n, U), 2n, C)
      expect(removeMarkAt(marks, 1n).map(m => m.seq)).toEqual([2n])
      expect(removeMarkAt(marks, 99n)).toBe(marks)
    })
  })

  describe('seed', () => {
    it('replaces marks, sorts/dedupes, and records the range', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 8n, type: C }, { seq: 2n, type: U }, { seq: 8n, type: C }], 1n, 10n)
        const st = s.get('a1')
        expect(st.loaded).toBe(true)
        expect(st.marks.map(m => m.seq)).toEqual([2n, 8n])
        expect(st.minSeq).toBe(1n)
        expect(st.seedMaxSeq).toBe(10n)
      })
    })

    it('ignores an UNSET (indeterminate) range for min/max, keeping the prior values', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 3n, type: U }], 2n, 20n)
        s.seed('a1', [{ seq: 3n, type: U }, { seq: 5n, type: U }], undefined, undefined)
        const st = s.get('a1')
        expect(st.minSeq).toBe(2n)
        expect(st.seedMaxSeq).toBe(20n)
        expect(st.marks.map(m => m.seq)).toEqual([3n, 5n])
      })
    })

    it('stays unloaded when the FIRST seed has an indeterminate (unset) range, then loads on a good reseed', () => {
      withStore((s) => {
        // Worker DB error: marks came back but the min/max range is unset. Installing marks
        // against the bogus 0n floor would mis-position every dot, so keep hidden.
        s.seed('a1', [{ seq: 5n, type: U }], undefined, undefined)
        expect(s.get('a1').loaded).toBe(false)
        // A later good reseed heals it and reveals the rail.
        s.seed('a1', [{ seq: 5n, type: U }], 4n, 8n)
        expect(s.get('a1').loaded).toBe(true)
        expect(s.get('a1').minSeq).toBe(4n)
      })
    })

    it('preserves a live mark noted BEYOND the snapshot horizon across a reseed (reconnect race)', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 2n, type: U }], 1n, 5n)
        // A send lands live (broadcast) AFTER the worker snapshotted ListMessageMarks but
        // before its response applies -- noteMark records seq 9n, past the snapshot's max.
        s.noteMark('a1', 9n, U)
        // The slightly-stale reseed (max=5n, no 9n) must NOT drop the freshly-noted 9n dot.
        s.seed('a1', [{ seq: 2n, type: U }], 1n, 5n)
        expect(s.get('a1').marks.map(m => m.seq)).toEqual([2n, 9n])
      })
    })

    it('a reseed still heals a delete WITHIN the snapshot horizon (wholesale replace at/below max)', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 2n, type: U }, { seq: 4n, type: U }], 1n, 5n)
        // While disconnected, seq 4n's message was deleted. The reseed (which read the DB
        // fresh, so 4n <= max is gone) drops it -- only marks BEYOND max are preserved.
        s.seed('a1', [{ seq: 2n, type: U }], 1n, 5n)
        expect(s.get('a1').marks.map(m => m.seq)).toEqual([2n])
      })
    })
  })

  describe('note mark', () => {
    it('inserts a mark and preserves its type', () => {
      withStore((s) => {
        s.seed('a1', [], 1n, 5n)
        s.noteMark('a1', 3n, C)
        expect(s.get('a1').marks).toEqual([{ seq: 3n, type: C }])
      })
    })

    it('rejects optimistic locals (seq 0n) and unmarked rows', () => {
      withStore((s) => {
        s.seed('a1', [], 1n, 5n)
        s.noteMark('a1', 0n, U)
        s.noteMark('a1', 4n, MarkType.UNSPECIFIED)
        expect(s.get('a1').marks).toEqual([])
      })
    })

    it('lowers minSeq when the agent was empty (first message of an empty agent)', () => {
      withStore((s) => {
        s.seed('a1', [], 0n, 0n) // empty agent
        s.noteMark('a1', 7n, U)
        expect(s.get('a1').minSeq).toBe(7n)
      })
    })

    it('does not raise minSeq for a later seq', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 2n, type: U }], 2n, 5n)
        s.noteMark('a1', 9n, U)
        expect(s.get('a1').minSeq).toBe(2n)
      })
    })

    it('bumps the live revision on a real insert but not on any no-op re-note, so a concurrent seed is not perturbed', () => {
      withStore((s) => {
        s.seed('a1', [], 1n, 5n)
        const r0 = s.liveRevision('a1')
        s.noteMark('a1', 3n, C) // a real insert
        const r1 = s.liveRevision('a1')
        expect(r1).toBeGreaterThan(r0)
        s.noteMark('a1', 3n, C) // idempotent re-note of the same seq
        s.noteMark('a1', 0n, U) // optimistic local, ignored
        s.noteMark('a1', 4n, MarkType.UNSPECIFIED) // unmarked row, ignored
        expect(s.liveRevision('a1')).toBe(r1) // none of the no-ops bumped the revision
      })
    })
  })

  describe('remove / forget', () => {
    it('remove drops a mark; forget clears the agent (and its live-revision counter) entirely', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 2n, type: U }, { seq: 4n, type: C }], 1n, 5n)
        s.remove('a1', 2n)
        expect(s.get('a1').marks.map(m => m.seq)).toEqual([4n])
        expect(s.liveRevision('a1')).toBeGreaterThan(0)
        s.forget('a1')
        // Back to the shared empty value (unseeded), and the live revision resets.
        expect(s.get('a1').loaded).toBe(false)
        expect(s.get('a1').marks).toEqual([])
        expect(s.liveRevision('a1')).toBe(0)
      })
    })

    it('bumps the live revision only when remove actually drops a mark, so an unmarked-row delete is not counted', () => {
      withStore((s) => {
        s.seed('a1', [{ seq: 2n, type: U }], 1n, 5n)
        const r0 = s.liveRevision('a1')
        s.remove('a1', 2n) // present -> dropped
        const r1 = s.liveRevision('a1')
        expect(r1).toBeGreaterThan(r0)
        s.remove('a1', 2n) // already gone
        s.remove('a1', 99n) // never marked
        expect(s.liveRevision('a1')).toBe(r1) // neither no-op bumped the revision
      })
    })
  })

  describe('resolve rail range', () => {
    /** resolveRailRange inputs with sensible defaults; override the field a case exercises. */
    function inputs(overrides: Partial<RailRangeInputs> = {}): RailRangeInputs {
      return {
        seededMinSeq: 10n,
        seedMaxSeq: 100n,
        liveMaxSeq: 0n,
        windowFirstSeq: undefined,
        windowLastSeq: undefined,
        hasOlderMessages: false,
        ...overrides,
      }
    }

    describe('min', () => {
      it('uses the loaded window head as the exact whole-history min when the oldest page is loaded', () => {
        expect(resolveRailRange(inputs({ hasOlderMessages: false, windowFirstSeq: 20n })).minSeq).toBe(20n)
      })

      it('falls back to the seeded min when the oldest is loaded but the window holds no server row', () => {
        expect(resolveRailRange(inputs({ hasOlderMessages: false, windowFirstSeq: undefined })).minSeq).toBe(10n)
      })

      it('uses the seeded min (ignoring the window head) while older history remains off-window', () => {
        expect(resolveRailRange(inputs({ hasOlderMessages: true, windowFirstSeq: 20n })).minSeq).toBe(10n)
      })
    })

    describe('max', () => {
      it('uses liveTail once populated (it rises past the window and falls on a tail delete)', () => {
        expect(resolveRailRange(inputs({ liveMaxSeq: 150n, windowLastSeq: 140n })).maxSeq).toBe(150n)
      })

      it('falls back to the seed max before liveTail is populated', () => {
        expect(resolveRailRange(inputs({ liveMaxSeq: 0n, seedMaxSeq: 100n, windowLastSeq: undefined })).maxSeq).toBe(100n)
      })

      it('lets the window tail win when rows persisted past the window before liveTail caught up', () => {
        expect(resolveRailRange(inputs({ liveMaxSeq: 0n, seedMaxSeq: 100n, windowLastSeq: 120n })).maxSeq).toBe(120n)
        // Even over a populated liveTail: the window tail is the freshest known max.
        expect(resolveRailRange(inputs({ liveMaxSeq: 150n, windowLastSeq: 200n })).maxSeq).toBe(200n)
      })

      it('ignores a window tail at or below the seeded/live max (undefined or smaller)', () => {
        expect(resolveRailRange(inputs({ liveMaxSeq: 150n, windowLastSeq: undefined })).maxSeq).toBe(150n)
        expect(resolveRailRange(inputs({ liveMaxSeq: 150n, windowLastSeq: 150n })).maxSeq).toBe(150n)
      })
    })
  })
})
