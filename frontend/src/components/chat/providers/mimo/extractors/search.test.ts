import { describe, expect, it } from 'vitest'
import { mimoGlobFiles, mimoGrepMatches } from './search'

const TRUNCATED = 'Results truncated: showing 1 of 3 matches (2 hidden). Consider using a more specific path or pattern.'
const PARTIAL = 'Some paths were inaccessible and skipped'

describe('mimoGrepMatches', () => {
  it('reads every match under its file', () => {
    expect(mimoGrepMatches('Found 3 matches\n/p/a.ts:\n  Line 1: a\n  Line 4: b\n\n/p/b.ts:\n  Line 2: c', 3)).toEqual({
      matches: [
        { filePath: '/p/a.ts', lineNumber: 1, text: 'a' },
        { filePath: '/p/a.ts', lineNumber: 4, text: 'b' },
        { filePath: '/p/b.ts', lineNumber: 2, text: 'c' },
      ],
    })
  })

  it('reads the first page of a truncated search, and its notice', () => {
    const text = `Found 3 matches (showing first 1)\n/p/a.ts:\n  Line 1: a\n\n(${TRUNCATED})`
    expect(mimoGrepMatches(text, 3)).toEqual({ matches: [{ filePath: '/p/a.ts', lineNumber: 1, text: 'a' }], notice: TRUNCATED })
  })

  // MiMo adds the notice when it could not read a part of the tree. The matches it
  // found are still the answer.
  it('reads a search that skipped paths it could not read', () => {
    expect(mimoGrepMatches(`Found 1 matches\n/p/a.ts:\n  Line 3: alpha\n\n(${PARTIAL})`, 1))
      .toEqual({ matches: [{ filePath: '/p/a.ts', lineNumber: 3, text: 'alpha' }], notice: PARTIAL })
  })

  it('states both notices of a truncated search that skipped paths', () => {
    const text = `Found 3 matches (showing first 1)\n/p/a.ts:\n  Line 1: a\n\n(${TRUNCATED})\n\n(${PARTIAL})`
    expect(mimoGrepMatches(text, 3)).toEqual({ matches: [{ filePath: '/p/a.ts', lineNumber: 1, text: 'a' }], notice: `${TRUNCATED} ${PARTIAL}` })
  })

  it('reads the empty answer', () => {
    expect(mimoGrepMatches('No files found', 0)).toEqual({ matches: [] })
    expect(mimoGrepMatches('  No files found\n', 0)).toEqual({ matches: [] })
  })

  it('reads a heading of zero matches as the empty answer', () => {
    expect(mimoGrepMatches('Found 0 matches', 0)).toEqual({ matches: [] })
  })

  // MiMo prints the path the platform gives: a drive letter or a network share on
  // Windows, where the lines also end with a carriage return.
  it('reads a Windows path and Windows line endings', () => {
    expect(mimoGrepMatches('Found 2 matches\r\nC:\\p\\a.ts:\r\n  Line 1: a\r\n\r\n\\\\host\\share\\b.ts:\r\n  Line 9: b', 2)).toEqual({
      matches: [
        { filePath: 'C:\\p\\a.ts', lineNumber: 1, text: 'a' },
        { filePath: '\\\\host\\share\\b.ts', lineNumber: 9, text: 'b' },
      ],
    })
  })

  it('keeps the text of a match whole, colons and indentation included', () => {
    expect(mimoGrepMatches('Found 1 matches\n/p/a.ts:\n  Line 3:   key: value', 1)).toEqual({ matches: [{ filePath: '/p/a.ts', lineNumber: 3, text: '  key: value' }] })
  })

  it.each([
    ['a count that disagrees', 'Found 2 matches\n/p/a.ts:\n  Line 1: a', 2],
    ['a heading of another total', 'Found 5 matches\n/p/a.ts:\n  Line 1: a', 1],
    ['a file with no match', 'Found 1 matches\n/p/a.ts:\n/p/b.ts:\n  Line 1: a', 1],
    ['a stray row', 'Found 1 matches\n/p/a.ts:\n  Line 1: a\nnoise', 1],
    ['a line number of zero', 'Found 1 matches\n/p/a.ts:\n  Line 0: a', 1],
    ['a negative count', 'No files found', -1],
    ['a match after the skipped-paths notice', `Found 2 matches\n/p/a.ts:\n  Line 1: a\n\n(${PARTIAL})\n/p/b.ts:\n  Line 2: b`, 2],
    ['a truncation notice after the skipped-paths notice', `Found 3 matches (showing first 1)\n/p/a.ts:\n  Line 1: a\n\n(${PARTIAL})\n\n(${TRUNCATED})`, 3],
    ['a truncation notice on a search that states no page', `Found 1 matches\n/p/a.ts:\n  Line 1: a\n\n(${TRUNCATED})`, 1],
    ['a skipped-paths notice repeated', `Found 1 matches\n/p/a.ts:\n  Line 1: a\n\n(${PARTIAL})\n\n(${PARTIAL})`, 1],
    ['a match before any file', 'Found 1 matches\n  Line 1: a', 1],
    ['a relative path', 'Found 1 matches\np/a.ts:\n  Line 1: a', 1],
    ['a count that is not a whole number', 'Found 1 matches\n/p/a.ts:\n  Line 1: a', 1.5],
    ['a count that is not a number', 'No files found', Number.NaN],
    ['a count past the safe integers', 'No files found', 2 ** 53],
    ['an empty answer that states a count', 'No files found', 1],
    ['an empty output', '', 0],
    ['a page larger than it printed', 'Found 3 matches (showing first 2)\n/p/a.ts:\n  Line 1: a', 3],
  ])('refuses %s', (_name, text, count) => {
    expect(mimoGrepMatches(text, count)).toBeNull()
  })
})

describe('mimoGlobFiles', () => {
  it('reads the listed files and the truncation notice', () => {
    expect(mimoGlobFiles('/p/a.ts\n/p/b.ts', 2)).toEqual({ files: ['/p/a.ts', '/p/b.ts'] })
    expect(mimoGlobFiles('/p/a.ts\n\n(Results are truncated: showing first 1 results. Consider using a more specific path or pattern.)', 1))
      .toEqual({ files: ['/p/a.ts'], notice: 'Results are truncated: showing first 1 results. Consider using a more specific path or pattern.' })
  })

  it('reads the empty answer', () => {
    expect(mimoGlobFiles('No files found', 0)).toEqual({ files: [] })
  })

  it('refuses a listing that disagrees with its count', () => {
    expect(mimoGlobFiles('/p/a.ts', 2)).toBeNull()
    expect(mimoGlobFiles('something', 0)).toBeNull()
  })

  it('reads Windows line endings and skips blank lines', () => {
    expect(mimoGlobFiles('C:\\p\\a.ts\r\n\r\nC:\\p\\b.ts\r\n', 2)).toEqual({ files: ['C:\\p\\a.ts', 'C:\\p\\b.ts'] })
  })

  it.each([
    ['a negative count', '/p/a.ts', -1],
    ['a count that is not a whole number', '/p/a.ts', 0.5],
    ['a count that is not a number', '/p/a.ts', Number.NaN],
    // A notice of another shape is no file. Counted as one, it would draw as a path.
    ['a notice of another shape', '/p/a.ts\n(Some paths were inaccessible and skipped)', 2],
    ['a truncation notice with too few files', '/p/a.ts\n\n(Results are truncated: showing first 2 results.)', 2],
    ['an empty output with a count', '', 1],
  ])('refuses %s', (_name, text, count) => {
    expect(mimoGlobFiles(text, count)).toBeNull()
  })
})
