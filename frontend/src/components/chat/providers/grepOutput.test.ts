import { describe, expect, it } from 'vitest'
import { grepMatches } from './grepOutput'

describe('grepMatches', () => {
  it('keeps every line that states a path and a line number', () => {
    const matches = grepMatches(['src/a.ts:1:first', 'src/b.ts:12:second'])
    expect(matches).toStrictEqual({ lines: ['src/a.ts:1:first', 'src/b.ts:12:second'], numFiles: 2 })
  })

  it('counts one file for several matches inside it', () => {
    const matches = grepMatches(['src/a.ts:1:first', 'src/a.ts:9:second', 'src/a.ts:40:third'])
    expect(matches.lines).toStrictEqual(['src/a.ts:1:first', 'src/a.ts:9:second', 'src/a.ts:40:third'])
    expect(matches.numFiles).toBe(1)
  })

  it('reads no match out of a heading, a notice or a blank line', () => {
    expect(grepMatches(['Found 2 matches', '', 'src/a.ts:1:first', '... (truncated)'])).toStrictEqual({
      lines: ['src/a.ts:1:first'],
      numFiles: 1,
    })
  })

  it('answers no match and no file for an empty list', () => {
    expect(grepMatches([])).toStrictEqual({ lines: [], numFiles: 0 })
  })

  // `''.split('\n')` is `['']`, so the one line a caller passes for an empty output must
  // count for nothing. A path of at least one character is what the format requires.
  it('reads no match out of a line that states no path', () => {
    expect(grepMatches(['', ':1:no path at all'])).toStrictEqual({ lines: [], numFiles: 0 })
  })

  it('reads no match out of a line whose line number is absent', () => {
    expect(grepMatches(['src/a.ts:first', 'src/a.ts::first'])).toStrictEqual({ lines: [], numFiles: 0 })
  })

  // The matched TEXT holds path punctuation of its own, and the path ends at the first
  // `:<digits>:` rather than at the last one.
  it('leaves a colon inside the matched text out of the path', () => {
    const matches = grepMatches(['src/a.ts:12:const url = \'http://example.com:8080/x\'', 'src/a.ts:13:const port = 8080'])
    expect(matches.numFiles).toBe(1)
  })

  it('keeps a drive letter inside the path', () => {
    const matches = grepMatches(['C:/src/a.ts:12:first', 'C:/src/b.ts:12:second'])
    expect(matches.numFiles).toBe(2)
  })

  it('counts a repeated match of one line once for the file it sits in', () => {
    expect(grepMatches(['src/a.ts:1:first', 'src/a.ts:1:first']).numFiles).toBe(1)
  })
})
