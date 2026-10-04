import { describe, expect, it } from 'vitest'
import { ampReadRequest, ampReadResult } from './read'

describe('ampReadRequest', () => {
  it('reads the path and the inclusive range', () => {
    expect(ampReadRequest({ path: '/work/a.ts', read_range: [10, 19] })).toEqual({ path: '/work/a.ts', offset: 10, limit: 10 })
    expect(ampReadRequest({ path: '/work/a.ts', read_range: [5, 5] })).toEqual({ path: '/work/a.ts', offset: 5, limit: 1 })
  })

  it('states the start alone when the end is absent or before it', () => {
    expect(ampReadRequest({ path: '/a', read_range: [5] })).toEqual({ path: '/a', offset: 5 })
    expect(ampReadRequest({ path: '/a', read_range: [5, 2] })).toEqual({ path: '/a', offset: 5 })
  })

  it('ignores a range that is not one', () => {
    for (const range of [undefined, [], [0, 3], [-1, 3], ['1', '3'], [1.5, 3]])
      expect(ampReadRequest({ path: '/a', read_range: range }), JSON.stringify(range)).toEqual({ path: '/a' })
  })
})

describe('ampReadResult', () => {
  it('reads the numbered lines', () => {
    expect(ampReadResult(JSON.stringify({ absolutePath: '/a', content: '1: alpha\n2: \n3: gamma: three' }))).toEqual({
      result: { lines: [{ num: 1, text: 'alpha' }, { num: 2, text: '' }, { num: 3, text: 'gamma: three' }], fallbackContent: '1: alpha\n2: \n3: gamma: three' },
      images: [],
    })
  })

  it('draws a body with an omission line as the text Amp sent', () => {
    const content = '1: a\n[... omitted lines 2 to 9 ...]\n10: b'
    expect(ampReadResult(JSON.stringify({ absolutePath: '/a', content }))).toEqual({ result: { lines: null, fallbackContent: content }, images: [] })
  })

  it('reads an empty file and a directory', () => {
    expect(ampReadResult(JSON.stringify({ absolutePath: '/a', content: '' }))).toEqual({ result: { lines: [], fallbackContent: '' }, images: [] })
    expect(ampReadResult(JSON.stringify({ absolutePath: '/d', content: 'a.ts\nb/', isDirectory: true, directoryEntries: ['a.ts', 'b/'] })))
      .toEqual({ result: { lines: null, fallbackContent: 'a.ts\nb/' }, images: [] })
  })

  it('reads an image as the picture', () => {
    const outcome = ampReadResult(JSON.stringify({ absolutePath: '/shot.png', content: 'iVBORw0KGgo=', isImage: true, imageInfo: { mimeType: 'image/png', size: 8 } }))
    expect(outcome?.result).toEqual({ lines: null, fallbackContent: '' })
    expect(outcome?.images).toHaveLength(1)
    expect(outcome?.images[0]).toMatchObject({ mimeType: 'image/png' })
  })

  it('answers null for a result that is not the record', () => {
    expect(ampReadResult('File not found')).toBeNull()
    expect(ampReadResult('{"absolutePath":"/a"}')).toBeNull()
  })
})
