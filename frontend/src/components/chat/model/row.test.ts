import { describe, expect, it } from 'vitest'
import { toolCallFixture } from '~/test-support/toolCallFixture'
import { rowDrawsRequest, rowDrawsResult, rowHasRequestRow, toolRowPosition } from './derivations'
import { toolCallRow } from './row'

const call = toolCallFixture('execute', {
  request: { command: 'ls -1' },
})

describe('toolCallRow', () => {
  // A row is never its own sibling. Six providers each restated this rule to fill the
  // flag, and one that restated it wrongly drew a result row with its own header
  // suppressed and no request row beside it to carry one.
  it('never states a request row as its own sibling', () => {
    const request = toolCallRow(call, 'request', { request: true })
    expect(rowHasRequestRow(request)).toBe(false)

    const closer = toolCallRow(call, 'result', { request: true })
    expect(rowHasRequestRow(closer)).toBe(true)
  })

  // An update sits after the request, so the request row is a real sibling of it.
  it('states the request row for a row after it', () => {
    expect(rowHasRequestRow(toolCallRow(call, 'update', { request: true }))).toBe(true)
  })

  it('states no sibling the span does not hold', () => {
    expect(rowHasRequestRow(toolCallRow(call, 'update', { request: false }))).toBe(false)
    expect(rowHasRequestRow(toolCallRow(call, 'result', { request: false }))).toBe(false)
  })

  // The flag the role rules out is ABSENT rather than false, so every reader must go
  // through the derivations. This pins that an absent flag reads as no.
  it('reads an absent flag as no', () => {
    const request = toolCallRow(call, 'request', { request: true })
    expect(request.role === 'request'
      ? request.hasRequestRow
      : true).toBeUndefined()
    expect(rowHasRequestRow(request)).toBe(false)
  })

  // A row knows nothing about the rows after it in its span, so no role carries a flag
  // about the result row. A request row that knew it would change once its result
  // arrived, and its first height would be wrong.
  it('states nothing about the result row on any role', () => {
    for (const role of ['request', 'update', 'result'] as const)
      expect(Object.keys(toolCallRow(call, role, { request: true }))).not.toContain('hasResultRow')
  })

  describe('toolRowPosition', () => {
    // The renderer view takes this whole rather than restating the fields, so the
    // union's rule reaches every reader instead of stopping at the row.
    // The KEYS, not just the values: `toEqual` ignores an undefined-valued property,
    // so a position that carried `hasRequestRow: undefined` on a request row would
    // pass a value comparison while still stating a field the role rules out.
    it('carries only the sibling the role admits', () => {
      const request = toolRowPosition(toolCallRow(call, 'request', { request: true }))
      expect(Object.keys(request)).toEqual(['role'])
      expect(request).toEqual({ role: 'request' })

      const closer = toolRowPosition(toolCallRow(call, 'result', { request: true }))
      expect(Object.keys(closer).sort()).toEqual(['hasRequestRow', 'role'])
      expect(closer).toEqual({ role: 'result', hasRequestRow: true })

      const update = toolRowPosition(toolCallRow(call, 'update', { request: false }))
      expect(Object.keys(update).sort()).toEqual(['hasRequestRow', 'role'])
      expect(update).toEqual({ role: 'update', hasRequestRow: false })
    })
  })

  describe('rowDrawsResult', () => {
    it('draws on the result row, with or without a request row beside it', () => {
      expect(rowDrawsResult(toolCallRow(call, 'result', { request: true }))).toBe(true)
      expect(rowDrawsResult(toolCallRow(call, 'result', { request: false }))).toBe(true)
    })

    // A request or update row never reads its result, so it has none to draw. The
    // pending row draws the preview of a call that still runs.
    it('never draws on a request or update row', () => {
      expect(rowDrawsResult(toolCallRow(call, 'request', { request: false }))).toBe(false)
      expect(rowDrawsResult(toolCallRow(call, 'update', { request: false }))).toBe(false)
      expect(rowDrawsResult(toolCallRow(call, 'update', { request: true }))).toBe(false)
    })
  })

  describe('rowDrawsRequest', () => {
    it('draws on the request row, and on a later row with no request row beside it', () => {
      expect(rowDrawsRequest(toolCallRow(call, 'request', { request: true }))).toBe(true)
      expect(rowDrawsRequest(toolCallRow(call, 'result', { request: false }))).toBe(true)
      expect(rowDrawsRequest(toolCallRow(call, 'update', { request: false }))).toBe(true)
    })

    it('leaves the request to the request row beside it', () => {
      expect(rowDrawsRequest(toolCallRow(call, 'result', { request: true }))).toBe(false)
      expect(rowDrawsRequest(toolCallRow(call, 'update', { request: true }))).toBe(false)
    })
  })
})
