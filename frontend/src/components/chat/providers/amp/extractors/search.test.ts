import { describe, expect, it } from 'vitest'
import { ampGlobResult, ampGrepResult } from './search'

describe('ampGlobResult', () => {
  it('reads a JSON list and a list of lines', () => {
    expect(ampGlobResult('["/w/a.ts","/w/b.ts"]')).toMatchObject({ filenames: ['/w/a.ts', '/w/b.ts'], numFiles: 2, empty: false })
    expect(ampGlobResult('/w/a.ts\n\n/w/b.ts\n')).toMatchObject({ filenames: ['/w/a.ts', '/w/b.ts'], numFiles: 2 })
  })

  it('reads a search that matched nothing as empty', () => {
    for (const text of ['[]', '', 'No results found.'])
      expect(ampGlobResult(text), text).toMatchObject({ filenames: [], numFiles: 0, empty: true, fallbackContent: text })
  })

  it('reads a list of lines with Windows line ends', () => {
    expect(ampGlobResult('/w/a.ts\r\n/w/b.ts\r\n').filenames).toEqual(['/w/a.ts', '/w/b.ts'])
  })

  it('keeps only the strings of a JSON list', () => {
    expect(ampGlobResult('["/w/a.ts",3,null,{"path":"/w/b.ts"}]')).toMatchObject({ filenames: ['/w/a.ts'], numFiles: 1, empty: false })
  })
})

describe('ampGrepResult', () => {
  it('reads each match with its file and its line', () => {
    const result = ampGrepResult('["/w/a.ts:3:const alpha = 1","/w/a.ts:9:alpha()","/w/b.ts:1:// alpha"]')
    expect(result.lines).toEqual([
      { filePath: '/w/a.ts', lineNumber: 3, text: 'const alpha = 1' },
      { filePath: '/w/a.ts', lineNumber: 9, text: 'alpha()' },
      { filePath: '/w/b.ts', lineNumber: 1, text: '// alpha' },
    ])
    expect(result.filenames).toEqual(['/w/a.ts', '/w/b.ts'])
    expect(result).toMatchObject({ numFiles: 2, numLines: 3, empty: false })
  })

  it('keeps a line that is not a match in the text, and counts only the matches', () => {
    const result = ampGrepResult('/w/a.ts:3:alpha\n(more results omitted)')
    expect(result.numLines).toBe(1)
    expect(result.content).toBe('/w/a.ts:3:alpha\n(more results omitted)')
    expect(result.empty).toBe(false)
  })

  it('reads a search that matched nothing as empty', () => {
    expect(ampGrepResult('No results found.')).toMatchObject({ lines: [], numLines: 0, empty: true })
    expect(ampGrepResult('[]')).toMatchObject({ empty: true })
  })

  // The file is the text before the FIRST `:<digits>:`, so a drive letter stays in the
  // path and a colon in the matched line stays in its text.
  it('reads a match whose path holds a drive letter and whose text holds a colon', () => {
    expect(ampGrepResult('["C:\\\\w\\\\a.ts:3:key: value"]').lines).toEqual([{ filePath: 'C:\\w\\a.ts', lineNumber: 3, text: 'key: value' }])
  })
})
