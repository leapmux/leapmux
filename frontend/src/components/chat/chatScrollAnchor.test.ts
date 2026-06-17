import type { AnchorOffsetGeometry, AnchorRow } from './chatScrollAnchor'
import { describe, expect, it } from 'vitest'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'

/**
 * A fake geometry over uniform rows of `rowHeight` with a constant `gap` below each
 * (except the last). Rows are positioned at index*(rowHeight+gap); ids are `m<seq>`.
 */
function fakeGeo(rows: AnchorRow[], rowHeight = 100, gap = 20): AnchorOffsetGeometry {
  const stride = rowHeight + gap
  return {
    list: rows,
    indexOfId: id => rows.findIndex(r => r.id === id),
    offsetOfIndex: i => Math.max(0, Math.min(i, rows.length)) * stride,
    heightOfIndex: () => rowHeight,
    gapAfter: i => (i >= rows.length - 1 ? 0 : gap),
    indexAtOffset: (y) => {
      // Largest index whose top offset <= y, clamped to [0, n-1].
      let idx = Math.floor(y / stride)
      if (idx < 0)
        idx = 0
      if (idx > rows.length - 1)
        idx = rows.length - 1
      return idx
    },
  }
}

function rows(seqs: number[]): AnchorRow[] {
  return seqs.map(s => ({ id: `m${s}`, seq: BigInt(s) }))
}

describe('chatscrollanchor', () => {
  it('captures and resolves a within-row anchor round-trip', () => {
    const geo = fakeGeo(rows([10, 20, 30]))
    // scrollTop 250 -> row index 2 (stride 120 -> [240, 360)), 10px into the body.
    const anchor = anchorAtOffset(geo, 250)
    expect(anchor).toEqual({ id: 'm30', offsetWithinRow: 10, basisHeight: 100, gapFraction: 0, seq: 30n })
    expect(resolveAnchorScrollTop(geo, anchor!)).toBe(250)
  })

  it('records and reproduces an in-gap position as a fraction of the gap', () => {
    const geo = fakeGeo(rows([10, 20, 30]))
    // scrollTop 110 -> row 0 body is [0,100), the 20px gap is [100,120); 110 is halfway.
    const anchor = anchorAtOffset(geo, 110)
    expect(anchor).toEqual({ id: 'm10', offsetWithinRow: 100, basisHeight: 100, gapFraction: 0.5, seq: 10n })
    expect(resolveAnchorScrollTop(geo, anchor!)).toBe(110)
  })

  it('returns null for an empty list, and resolves null for a missing id', () => {
    expect(anchorAtOffset(fakeGeo([]), 0)).toBeNull()
    expect(resolveAnchorScrollTop(fakeGeo(rows([10])), { id: 'gone', offsetWithinRow: 0 })).toBeNull()
  })

  it('resolves proportionally when the row height changed since capture', () => {
    const geo = fakeGeo(rows([10, 20]), 100)
    // Anchor captured at the middle of a 200px-basis row; current height is 100.
    const top = resolveAnchorScrollTop(geo, { id: 'm20', offsetWithinRow: 100, basisHeight: 200, seq: 20n })
    // offsetOfIndex(1)=120, within = 100 * (100/200) = 50 -> 170.
    expect(top).toBe(170)
  })

  describe('resolveNearestAnchorScrollTop (trimmed-row recovery)', () => {
    it('returns the exact position when the row still resolves', () => {
      const geo = fakeGeo(rows([10, 20, 30]))
      expect(resolveNearestAnchorScrollTop(geo, { id: 'm20', offsetWithinRow: 0, seq: 20n })).toBe(120)
    })

    it('lands on the nearest surviving row by seq when the row was trimmed', () => {
      const geo = fakeGeo(rows([10, 20, 30, 40, 50]))
      // 35 is equidistant from 30 and 40; the scan keeps the FIRST minimum (30 -> 240).
      expect(resolveNearestAnchorScrollTop(geo, { id: 'gone', offsetWithinRow: 0, seq: 35n })).toBe(240)
      // older than the window -> oldest survivor (seq 10 -> 0).
      expect(resolveNearestAnchorScrollTop(geo, { id: 'gone', offsetWithinRow: 0, seq: 5n })).toBe(0)
      // newer than the window -> newest survivor (seq 50 -> 480).
      expect(resolveNearestAnchorScrollTop(geo, { id: 'gone', offsetWithinRow: 0, seq: 99n })).toBe(480)
    })

    it('skips trailing optimistic locals (seq 0n)', () => {
      const geo = fakeGeo([...rows([10, 20]), { id: 'local', seq: 0n }])
      // seq 2 is closest to the local's 0n, but locals are skipped -> seq 10 (offset 0).
      expect(resolveNearestAnchorScrollTop(geo, { id: 'gone', offsetWithinRow: 0, seq: 2n })).toBe(0)
    })

    it('returns null with no seq, and with no surviving server row', () => {
      expect(resolveNearestAnchorScrollTop(fakeGeo(rows([10])), { id: 'gone', offsetWithinRow: 0 })).toBeNull()
      const localsOnly = fakeGeo([{ id: 'local', seq: 0n }])
      expect(resolveNearestAnchorScrollTop(localsOnly, { id: 'gone', offsetWithinRow: 0, seq: 5n })).toBeNull()
    })

    it('returns null (does not land on the oldest row) for a reconciled-local anchor (seq 0n)', () => {
      // An anchor captured on an optimistic local carries seq 0n. Once the local
      // reconciles its id changes, so the exact resolve fails and the nearest scan runs.
      // A 0n seq has no ordering against server rows: the delta to every survivor would
      // equal that survivor's own seq, picking the OLDEST row and yanking the reader to
      // the top of history. Bail to null instead (caller snaps to the tail, where the
      // local lived).
      const geo = fakeGeo(rows([10, 20, 30]))
      expect(resolveNearestAnchorScrollTop(geo, { id: 'gone', offsetWithinRow: 0, seq: 0n })).toBeNull()
    })
  })
})
