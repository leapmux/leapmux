import { describe, expect, it } from 'vitest'
import { claudeGlobFromToolResult, claudeGrepFromToolResult, parseRawGrepGlobResult } from './grepGlob'

describe('parseRawGrepGlobResult', () => {
  it('parses "Found N files" summary and file list', () => {
    const raw = 'Found 2 files\n/a.ts\n/b.ts'
    expect(parseRawGrepGlobResult(raw, 'Glob')).toEqual({
      numFiles: 2,
      numLines: 0,
      filenames: ['/a.ts', '/b.ts'],
      content: '',
    })
  })

  it('parses "N matches in M files" summary for Grep when data lines are file paths', () => {
    const raw = '5 matches in 3 files\n/a\n/b\n/c'
    // The parser preserves numFiles from the summary; numLines is reset to
    // 0 in the file-list branch (existing behavior).
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 3,
      numLines: 0,
      filenames: ['/a', '/b', '/c'],
      content: '',
    })
  })

  it('returns content lines when Grep output looks like content mode', () => {
    const raw = '/a:1:foo\n/a:2:bar'
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 0,
      numLines: 2,
      filenames: [],
      content: '/a:1:foo\n/a:2:bar',
    })
  })

  it('handles "No matches found" / "No files found"', () => {
    expect(parseRawGrepGlobResult('No matches found', 'Grep')).toEqual({
      numFiles: 0,
      numLines: 0,
      filenames: [],
      content: '',
    })
  })
})

describe('claudeGrepFromToolResult', () => {
  it('builds source from structured result', () => {
    const source = claudeGrepFromToolResult({
      numFiles: 3,
      numLines: 5,
      filenames: ['/a', '/b', '/c'],
      content: 'matched line',
      mode: 'content',
      appliedLimit: 100,
    }, 'fallback')
    expect(source).toMatchObject({
      variant: 'grep',
      numFiles: 3,
      numLines: 5,
      filenames: ['/a', '/b', '/c'],
      content: 'matched line',
      mode: 'content',
      truncated: true,
      fallbackContent: 'fallback',
    })
  })

  it('marks truncated=false when appliedLimit is missing', () => {
    const source = claudeGrepFromToolResult({ numFiles: 1, filenames: ['/a'] }, '')
    expect(source.truncated).toBe(false)
  })

  it('falls back to raw parsing when toolUseResult is null', () => {
    const source = claudeGrepFromToolResult(null, 'Found 1 file\n/a.ts')
    expect(source.numFiles).toBe(1)
    expect(source.filenames).toEqual(['/a.ts'])
  })
})

describe('claudeGlobFromToolResult', () => {
  it('builds source from structured result with truncated and durationMs', () => {
    const source = claudeGlobFromToolResult({
      filenames: ['/a', '/b'],
      durationMs: 12,
      truncated: true,
    }, '')
    expect(source).toMatchObject({
      variant: 'glob',
      numFiles: 2,
      numLines: 0,
      truncated: true,
      durationMs: 12,
    })
  })

  it('falls back to raw parsing when toolUseResult is null', () => {
    const source = claudeGlobFromToolResult(null, 'Found 2 files\n/a\n/b')
    expect(source.numFiles).toBe(2)
    expect(source.filenames).toEqual(['/a', '/b'])
  })
})
