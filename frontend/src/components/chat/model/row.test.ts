import { describe, expect, it } from 'vitest'
import { toolCallFixture } from '~/test-support/toolCallFixture'
import { rowDrawsResult, rowHasRequestRow, rowHasResultRow, toolRowPosition } from './derivations'
import { toolCallRow } from './row'

const call = toolCallFixture('execute', { request: { command: 'ls -1' } })

describe('toolCallRow', () => {
  // A row is never its own sibling. Six providers each restated this rule to fill the
  // two flags, and one that restated it wrongly drew a result row with its own header
  // suppressed and no request row beside it to carry one.
  it('never states a row as its own sibling', () => {
    const request = toolCallRow(call, 'request', { request: true, result: true })
    expect(rowHasRequestRow(request)).toBe(false)
    expect(rowHasResultRow(request)).toBe(true)

    const closer = toolCallRow(call, 'result', { request: true, result: true })
    expect(rowHasRequestRow(closer)).toBe(true)
    expect(rowHasResultRow(closer)).toBe(false)
  })

  // An update sits between the two, so both siblings are real for it.
  it('states both siblings for a row between them', () => {
    const update = toolCallRow(call, 'update', { request: true, result: true })
    expect(rowHasRequestRow(update)).toBe(true)
    expect(rowHasResultRow(update)).toBe(true)
  })

  it('states no sibling the span does not hold', () => {
    const alone = toolCallRow(call, 'update', { request: false, result: false })
    expect(rowHasRequestRow(alone)).toBe(false)
    expect(rowHasResultRow(alone)).toBe(false)
  })

  // The flag the role rules out is ABSENT rather than false, so every reader must go
  // through the derivations. This pins that an absent flag reads as no.
  it('reads an absent flag as no', () => {
    const request = toolCallRow(call, 'request', { request: true, result: false })
    expect(request.role === 'request' ? request.hasRequestRow : true).toBeUndefined()
    expect(rowHasRequestRow(request)).toBe(false)
  })

  describe('toolRowPosition', () => {
    // The renderer view takes this whole rather than restating the three fields, so
    // the union's rule reaches every reader instead of stopping at the row.
    // The KEYS, not just the values: `toEqual` ignores an undefined-valued property,
    // so a position that carried `hasRequestRow: undefined` on a request row would
    // pass a value comparison while still stating a field the role rules out.
    it('carries only the siblings the role admits', () => {
      const request = toolRowPosition(toolCallRow(call, 'request', { request: true, result: true }))
      expect(Object.keys(request).sort()).toEqual(['hasResultRow', 'role'])
      expect(request).toEqual({ role: 'request', hasResultRow: true })

      const closer = toolRowPosition(toolCallRow(call, 'result', { request: true, result: true }))
      expect(Object.keys(closer).sort()).toEqual(['hasRequestRow', 'role'])
      expect(closer).toEqual({ role: 'result', hasRequestRow: true })

      const update = toolRowPosition(toolCallRow(call, 'update', { request: true, result: false }))
      expect(Object.keys(update).sort()).toEqual(['hasRequestRow', 'hasResultRow', 'role'])
      expect(update).toEqual({ role: 'update', hasRequestRow: true, hasResultRow: false })
    })
  })

  describe('rowDrawsResult', () => {
    it('draws on the result row, and on any row with no result row beside it', () => {
      expect(rowDrawsResult(toolCallRow(call, 'result', { request: true, result: false }))).toBe(true)
      expect(rowDrawsResult(toolCallRow(call, 'request', { request: false, result: false }))).toBe(true)
      expect(rowDrawsResult(toolCallRow(call, 'update', { request: true, result: false }))).toBe(true)
    })

    it('leaves the result to the row beside it', () => {
      expect(rowDrawsResult(toolCallRow(call, 'request', { request: false, result: true }))).toBe(false)
      expect(rowDrawsResult(toolCallRow(call, 'update', { request: true, result: true }))).toBe(false)
    })
  })
})
