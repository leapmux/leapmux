import { describe, expect, it } from 'vitest'
import { TabDisplayMode, TabFileViewMode } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { decodeDisplayMode, decodeFileDiffBase, decodeFileViewMode, encodeDisplayMode, encodeFileViewMode } from './tabPresentation'

describe('tab presentation', () => {
  it.each([
    ['render', 1],
    ['source', 2],
    ['split', 3],
  ] as const)('round-trips display mode %s as %i', (mode, value) => {
    expect(encodeDisplayMode(mode)).toBe(value)
    expect(decodeDisplayMode(value)).toBe(mode)
  })

  it.each([
    ['working', 1],
    ['head', 2],
    ['staged', 3],
    ['unified-diff', 4],
    ['split-diff', 5],
  ] as const)('round-trips file view mode %s as %i', (mode, value) => {
    expect(encodeFileViewMode(mode)).toBe(value)
    expect(decodeFileViewMode(value)).toBe(mode)
  })

  it.each([
    undefined,
    0,
    -1,
    4,
    5,
    Number.MAX_SAFE_INTEGER,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ])('ignores an unknown display preference %s', (value) => {
    expect(decodeDisplayMode(value)).toBeUndefined()
  })

  it.each([
    undefined,
    0,
    -1,
    6,
    Number.MAX_SAFE_INTEGER,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ])('ignores an unknown file view preference %s', (value) => {
    expect(decodeFileViewMode(value)).toBeUndefined()
  })

  it.each(['head-vs-working', 'head-vs-staged'] as const)('preserves diff base %s', (value) => {
    expect(decodeFileDiffBase(value)).toBe(value)
  })

  it.each([
    ['absent', undefined],
    ['empty', ''],
    ['unknown', 'HEAD'],
    ['whitespace', 'head-vs-working '],
    [
      'large',
      'x'.repeat(10000),
    ],
  ])('ignores an unknown diff base %s', (_label, value) => {
    expect(decodeFileDiffBase(value)).toBeUndefined()
  })
})

describe('presentation enum coverage', () => {
  it('maps every nonzero display enum exactly once', () => {
    const mapped = (['render', 'source', 'split'] as const).map(encodeDisplayMode)
    const declared = Object.values(TabDisplayMode).filter(value => typeof value === 'number' && value !== 0)
    expect(new Set(mapped).size).toBe(mapped.length)
    expect(mapped.toSorted()).toEqual(declared.toSorted())
  })

  it('maps every nonzero file view enum exactly once', () => {
    const mapped = ([
      'working',
      'head',
      'staged',
      'unified-diff',
      'split-diff',
    ] as const).map(encodeFileViewMode)
    const declared = Object.values(TabFileViewMode).filter(value => typeof value === 'number' && value !== 0)
    expect(new Set(mapped).size).toBe(mapped.length)
    expect(mapped.toSorted()).toEqual(declared.toSorted())
  })
})
