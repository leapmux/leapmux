import { describe, expect, it } from 'vitest'
import { clineReadRequest, clineReadResult } from './read'

describe('clineReadRequest', () => {
  it('reads one file with its inclusive line range', () => {
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 3, end_line: 7 }] })).toEqual({ path: '/w/a.ts', offset: 3, limit: 5 })
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 3 }] })).toEqual({ path: '/w/a.ts', offset: 3 })
    expect(clineReadRequest({ files: [{ path: '/w/a.ts' }] })).toEqual({ path: '/w/a.ts' })
  })

  it('ignores a range that is not one', () => {
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 0 }] })).toEqual({ path: '/w/a.ts' })
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 9, end_line: 2 }] })).toEqual({ path: '/w/a.ts', offset: 9 })
    for (const start of [-1, 1.5, '3'])
      expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: start, end_line: 7 }] }), JSON.stringify(start)).toEqual({ path: '/w/a.ts' })
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 3, end_line: 4.5 }] })).toEqual({ path: '/w/a.ts', offset: 3 })
  })

  // An entry that is not a record states no file, so a list of them states none and
  // the bare path decides.
  it('reads a file list with no record as no list', () => {
    expect(clineReadRequest({ files: ['/w/a.ts', 3], path: '/w/b.ts' })).toEqual({ path: '/w/b.ts' })
    expect(clineReadRequest({ files: [] })).toEqual({ path: '' })
  })

  it('lists several files and takes no range', () => {
    expect(clineReadRequest({ files: [{ path: '/w/a.ts', start_line: 1 }, { path: '/w/b.ts' }] })).toEqual({ path: '/w/a.ts, /w/b.ts' })
  })

  it('reads a bare path', () => {
    expect(clineReadRequest({ path: '/w/a.ts' })).toEqual({ path: '/w/a.ts' })
  })
})

describe('clineReadResult', () => {
  it('reads the numbered lines of one file', () => {
    expect(clineReadResult([{ query: '/w/a.ts', result: ' 9 | alpha\n10 | beta\n', success: true }])).toEqual({
      lines: [{ num: 9, text: 'alpha' }, { num: 10, text: 'beta' }],
      fallbackContent: ' 9 | alpha\n10 | beta\n',
    })
  })

  it('keeps text that is not numbered', () => {
    expect(clineReadResult([{ query: '/w/a.ts', result: 'no numbers', success: true }])).toEqual({ lines: null, fallbackContent: 'no numbers' })
  })

  it('reads an empty file and a failed read', () => {
    expect(clineReadResult([{ query: '/w/a.ts', result: '', success: true }])).toEqual({ lines: [], fallbackContent: '' })
    expect(clineReadResult([{ query: '/w/a.ts', result: '', error: 'Not found', success: false }])).toEqual({ lines: null, fallbackContent: 'Not found' })
  })

  it('reads several files as text under their queries', () => {
    expect(clineReadResult([
      { query: '/w/a.ts', result: '1 | a', success: true },
      { query: '/w/b.ts', result: '', error: 'Not found', success: false },
    ])).toEqual({ lines: null, fallbackContent: '/w/a.ts\n1 | a\n\n/w/b.ts\nNot found' })
  })

  it('reads nothing from a result with no record', () => {
    expect(clineReadResult('text')).toBeNull()
  })
})
