import { describe, expect, it } from 'vitest'
import { clineSearchRequest, clineSearchResult } from './search'

describe('clineSearchRequest', () => {
  it('joins the patterns of one call', () => {
    expect(clineSearchRequest({ queries: ['alpha', 'beta'] })).toEqual({ pattern: 'alpha | beta', paths: [] })
    expect(clineSearchRequest({ query: 'alpha' })).toEqual({ pattern: 'alpha', paths: [] })
  })
})

describe('clineSearchResult', () => {
  it('reads each match of each search', () => {
    const result = clineSearchResult([
      { query: 'alpha', result: 'Found 2 results for pattern: alpha\n/w/a.ts:3:7\n/w/b.ts:9:1', success: true },
      { query: 'beta', result: 'Found 1 result for pattern: beta\n/w/a.ts:5:2', success: true },
    ])
    expect(result.filenames).toEqual(['/w/a.ts', '/w/b.ts'])
    expect(result.lines).toEqual([
      { filePath: '/w/a.ts', lineNumber: 3, text: '' },
      { filePath: '/w/b.ts', lineNumber: 9, text: '' },
      { filePath: '/w/a.ts', lineNumber: 5, text: '' },
    ])
    expect(result.numFiles).toBe(2)
    expect(result.matchCount).toBe(3)
    expect(result.empty).toBe(false)
  })

  it('states a search that matched nothing as empty', () => {
    const result = clineSearchResult([{ query: 'zeta', result: 'No results found for pattern: zeta\nSearched 12 files.', success: true }])
    expect(result.empty).toBe(true)
    expect(result.fallbackContent).toContain('No results found')
  })

  it('states a failed search as text, and not as empty', () => {
    const result = clineSearchResult([{ query: '(', result: '', error: 'Invalid pattern', success: false }])
    expect(result.empty).toBe(false)
    expect(result.content).toBe('Invalid pattern')
  })

  // No record means no search ran, which is not a search that matched nothing.
  it('states a result with no record as not empty', () => {
    for (const output of [[], undefined, 'plain text'])
      expect(clineSearchResult(output), JSON.stringify(output)).toMatchObject({ filenames: [], numLines: 0, empty: false, content: '' })
  })

  // The line and the column are the LAST two numbers, so a drive letter stays in the path.
  it('reads a match whose path holds a drive letter', () => {
    const result = clineSearchResult([{ query: 'alpha', result: 'Found 1 result for pattern: alpha\nC:\\w\\a.ts:3:7', success: true }])
    expect(result.lines).toEqual([{ filePath: 'C:\\w\\a.ts', lineNumber: 3, text: '' }])
  })

  it('reads the records of a stored result, which Cline keeps as JSON text', () => {
    expect(clineSearchResult(JSON.stringify([{ query: 'alpha', result: '/w/a.ts:3:7', success: true }])).filenames).toEqual(['/w/a.ts'])
  })
})
