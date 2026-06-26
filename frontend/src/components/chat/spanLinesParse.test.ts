import { describe, expect, it } from 'vitest'
import { parseSpanLines } from './spanLinesParse'

describe('spanLinesParse', () => {
  describe('parseSpanLines', () => {
    it('returns [] for an empty, undefined, or "[]" payload', () => {
      expect(parseSpanLines(undefined)).toEqual([])
      expect(parseSpanLines('')).toEqual([])
      expect(parseSpanLines('[]')).toEqual([])
    })

    it('returns [] for malformed JSON instead of throwing', () => {
      expect(parseSpanLines('{not json')).toEqual([])
    })

    it('returns [] for a well-formed NON-array payload (number, string, object)', () => {
      expect(parseSpanLines('5')).toEqual([])
      expect(parseSpanLines('"x"')).toEqual([])
      expect(parseSpanLines('{}')).toEqual([])
    })

    it('keeps real SpanLine columns and the null sentinel (a blank column)', () => {
      const raw = JSON.stringify([{ type: 'rail', color: 'red' }, null, { type: 'gap' }])
      expect(parseSpanLines(raw)).toEqual([{ type: 'rail', color: 'red' }, null, { type: 'gap' }])
    })

    it('drops primitive and typeless elements from a mixed array', () => {
      // `[5, "x"]`-style junk and a bare `{}` would otherwise render as colorless
      // columns and wrongly flip hasSpanLines; only the valid column survives. Also
      // covers the column predicate's rejections directly: a primitive (5, 'x'), a
      // nested array, a typeless object ({}, {notType}), and a non-STRING `type`
      // ({type: 7}) are all dropped, while the null sentinel is kept.
      const raw = JSON.stringify([5, 'x', [], {}, { notType: 1 }, { type: 7 }, null, { type: 'rail' }])
      expect(parseSpanLines(raw)).toEqual([null, { type: 'rail' }])
    })
  })
})
