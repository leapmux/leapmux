import type { ChatRowItem } from './chatRowItem'
import { describe, expect, it } from 'vitest'
import { sameChatRowItems } from './chatRowItem'

describe('sameChatRowItems', () => {
  const base: ChatRowItem = {
    id: 'm1',
    hasSpanLines: false,
    heightKey: 'k1',
    seq: 1n,
    kind: 'user_text',
  }

  it('is true for arrays equal on the geometry fields', () => {
    // A fresh array with the same geometry fields, as a streaming delta builds it, keeps
    // the array of the memo.
    expect(sameChatRowItems([
      { ...base },
      {
        ...base,
        id: 'm2',
        hasSpanLines: true,
        heightKey: 'k2',
      },
    ], [
      { ...base },
      {
        ...base,
        id: 'm2',
        hasSpanLines: true,
        heightKey: 'k2',
      },
    ])).toBe(true)
  })

  it('ignores non-geometry fields (seq) when they differ', () => {
    // seq is an anchor label, not an offset input, so a row that differs ONLY there
    // must still compare equal (else the memo would needlessly rebuild geometry on
    // every streaming delta).
    const other: ChatRowItem = { ...base, seq: 999n }
    expect(sameChatRowItems([base], [other])).toBe(true)
  })

  it('is false when any geometry field differs', () => {
    expect(sameChatRowItems([base], [
      { ...base, id: 'm2' },
    ])).toBe(false)
    expect(sameChatRowItems([base], [
      { ...base, hasSpanLines: true },
    ])).toBe(false)
    expect(sameChatRowItems([base], [
      { ...base, heightKey: 'k2' },
    ])).toBe(false)
    // kind decides the gap between two adjacent bands, so a kind change is geometry-relevant.
    expect(sameChatRowItems([base], [
      { ...base, kind: 'tool_result' },
    ])).toBe(false)
  })

  it('compares an absent optional field as a value', () => {
    const bare: ChatRowItem = { id: 'm1', hasSpanLines: false }
    // Two rows that state no height key and no kind are equal.
    expect(sameChatRowItems([bare], [
      { ...bare },
    ])).toBe(true)
    // A row that states a height key differs from a row that states none.
    expect(sameChatRowItems([bare], [
      { ...bare, heightKey: 'k1' },
    ])).toBe(false)
  })

  it('is false for different lengths and true for the same reference', () => {
    expect(sameChatRowItems([base], [base, base])).toBe(false)
    expect(sameChatRowItems([base], [])).toBe(false)
    const arr = [base]
    expect(sameChatRowItems(arr, arr)).toBe(true)
  })

  it('is true for two empty arrays', () => {
    expect(sameChatRowItems([], [])).toBe(true)
  })
})
