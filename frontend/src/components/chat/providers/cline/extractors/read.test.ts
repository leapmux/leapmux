import { describe, expect, it } from 'vitest'
import { clineReadImages, clineReadRequest, clineReadResult } from './read'

describe('clineReadImages', () => {
  it('keeps valid images in operation order and preserves accompanying text', () => {
    const output = [
      {
        query: '/w/a.png',
        success: true,
        result: [
          'First image',
          { type: 'image', data: 'YQ==', mediaType: 'image/png' },
          { type: 'image', data: 'Yg==', mediaType: 'image/webp' },
        ],
      },
      {
        query: '/w/b.png',
        success: true,
        result: [
          'Second image',
          { type: 'image', data: 'Yw==', mediaType: 'image/png' },
        ],
      },
    ]
    expect(clineReadImages(JSON.stringify(output), '/fallback')).toEqual([
      { data: 'YQ==', mimeType: 'image/png', filePath: '/w/a.png' },
      { data: 'Yg==', mimeType: 'image/webp', filePath: '/w/a.png' },
      { data: 'Yw==', mimeType: 'image/png', filePath: '/w/b.png' },
    ])
    expect(clineReadResult(output)?.fallbackContent).toBe('/w/a.png\nFirst image\n\n/w/b.png\nSecond image')
  })

  it('ignores failed operations and invalid image parts without dropping a valid sibling', () => {
    const valid = { type: 'image', data: 'YQ==', mediaType: 'image/png' }
    const output = [
      {
        query: '/w/failed.png',
        success: false,
        result: [valid],
      },
      {
        query: '/w/valid.png',
        result: [
          null,
          0,
          {},
          { ...valid, data: '' },
          { ...valid, data: 0 },
          { ...valid, mediaType: 'text/plain' },
          { ...valid, mediaType: null },
          { ...valid, type: 'text' },
          valid,
        ],
      },
    ]
    expect(clineReadImages(output, '')).toEqual([
      { data: 'YQ==', mimeType: 'image/png', filePath: '/w/valid.png' },
    ])
    for (const empty of [
      undefined,
      null,
      '',
      'bad JSON',
      [],
      {},
    ])
      expect(clineReadImages(empty, '/fallback')).toEqual([])
  })

  it('uses the request path only for a single operation that omits its query', () => {
    const image = { type: 'image', data: 'YQ==', mediaType: 'image/png' }
    expect(clineReadImages([
      {
        result: [image],
      },
    ], '/w/a.png')).toEqual([
      { data: 'YQ==', mimeType: 'image/png', filePath: '/w/a.png' },
    ])
    expect(clineReadImages([
      {
        result: [image],
      },
      {
        result: [image],
      },
    ], '/w/a.png, /w/b.png')).toEqual([
      { data: 'YQ==', mimeType: 'image/png' },
      { data: 'YQ==', mimeType: 'image/png' },
    ])
  })
})

describe('clineReadRequest', () => {
  it('reads one file with its inclusive line range', () => {
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 3, end_line: 7 },
      ],
    })).toEqual({ path: '/w/a.ts', offset: 3, limit: 5 })
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 3 },
      ],
    })).toEqual({ path: '/w/a.ts', offset: 3 })
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts' },
      ],
    })).toEqual({ path: '/w/a.ts' })
  })

  it('ignores a range that is not one', () => {
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 0 },
      ],
    })).toEqual({ path: '/w/a.ts' })
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 9, end_line: 2 },
      ],
    })).toEqual({ path: '/w/a.ts', offset: 9 })
    for (const start of [-1, 1.5, '3']) {
      expect(clineReadRequest({
        files: [
          { path: '/w/a.ts', start_line: start, end_line: 7 },
        ],
      }), JSON.stringify(start)).toEqual({ path: '/w/a.ts' })
    }
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 3, end_line: 4.5 },
      ],
    })).toEqual({ path: '/w/a.ts', offset: 3 })
  })

  // An entry that is not a record states no file, so a list of them states none and
  // the bare path decides.
  it('reads a file list with no record as no list', () => {
    expect(clineReadRequest({
      files: ['/w/a.ts', 3],
      path: '/w/b.ts',
    })).toEqual({ path: '/w/b.ts' })
    expect(clineReadRequest({ files: [] })).toEqual({ path: '' })
  })

  it('lists several files and takes no range', () => {
    expect(clineReadRequest({
      files: [
        { path: '/w/a.ts', start_line: 1 },
        { path: '/w/b.ts' },
      ],
    })).toEqual({ path: '/w/a.ts, /w/b.ts' })
  })

  it('reads a bare path', () => {
    expect(clineReadRequest({ path: '/w/a.ts' })).toEqual({ path: '/w/a.ts' })
  })
})

describe('clineReadResult', () => {
  it('reads the numbered lines of one file', () => {
    expect(clineReadResult([
      { query: '/w/a.ts', result: ' 9 | alpha\n10 | beta\n', success: true },
    ])).toEqual({
      lines: [
        { num: 9, text: 'alpha' },
        { num: 10, text: 'beta' },
      ],
      fallbackContent: ' 9 | alpha\n10 | beta\n',
    })
  })

  it('keeps text that is not numbered', () => {
    expect(clineReadResult([
      { query: '/w/a.ts', result: 'no numbers', success: true },
    ])).toEqual({ lines: null, fallbackContent: 'no numbers' })
  })

  it('reads an empty file and a failed read', () => {
    expect(clineReadResult([
      { query: '/w/a.ts', result: '', success: true },
    ])).toEqual({ lines: [], fallbackContent: '' })
    expect(clineReadResult([
      {
        query: '/w/a.ts',
        result: '',
        error: 'Not found',
        success: false,
      },
    ])).toEqual({ lines: null, fallbackContent: 'Not found' })
  })

  it('reads several files as text under their queries', () => {
    expect(clineReadResult([
      { query: '/w/a.ts', result: '1 | a', success: true },
      {
        query: '/w/b.ts',
        result: '',
        error: 'Not found',
        success: false,
      },
    ])).toEqual({ lines: null, fallbackContent: '/w/a.ts\n1 | a\n\n/w/b.ts\nNot found' })
  })

  it('reads nothing from a result with no record', () => {
    expect(clineReadResult('text')).toBeNull()
  })
})
