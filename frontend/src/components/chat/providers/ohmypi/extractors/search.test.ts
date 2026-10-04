import { describe, expect, it } from 'vitest'
import { ohMyPiGlobResult, ohMyPiGrepResult, ohMyPiSearchPattern, ohMyPiTextSearchResult } from './search'

describe('ohMyPiGrepResult', () => {
  it('reads the matching lines and omp\'s own counts', () => {
    // omp 18.2.11's own result (probe).
    const text = '# notes.txt#C789\n*1:alpha one\n 2:beta two\n 3:gamma three'
    const result = ohMyPiGrepResult(text, { matchCount: 1, fileCount: 1, files: ['notes.txt'], truncated: false })
    expect(result.lines).toEqual([{ filePath: 'notes.txt', lineNumber: 1, text: 'alpha one' }])
    expect(result).toMatchObject({ filenames: ['notes.txt'], numFiles: 1, numLines: 1, matchCount: 1, truncated: false, empty: false, content: text })
  })

  it('reads the matches of several files', () => {
    const result = ohMyPiGrepResult('# a.ts#0000\n*3:x\n\n# b.ts#1111\n 7:ctx\n*8:x again', { matchCount: 2, fileCount: 2, files: ['a.ts', 'b.ts'] })
    expect(result.lines).toEqual([
      { filePath: 'a.ts', lineNumber: 3, text: 'x' },
      { filePath: 'b.ts', lineNumber: 8, text: 'x again' },
    ])
  })

  it('reads the path of each file from omp\'s folded tree, one `#` for each level', () => {
    // omp 18.2.11's layout (`formatGroupedFiles`): a directory header ends in `/`,
    // and a file header under it states the file's name alone.
    const text = [
      '# src/',
      '## a.ts#1A2B',
      '*3:needle one',
      '',
      '## lib/',
      '### b.ts#FFFF',
      ' 7:context',
      '*8:needle two',
      '',
      '# README.md#0000',
      '*1:needle three',
    ].join('\n')
    expect(ohMyPiGrepResult(text, { matchCount: 3 }).lines).toEqual([
      { filePath: 'src/a.ts', lineNumber: 3, text: 'needle one' },
      { filePath: 'src/lib/b.ts', lineNumber: 8, text: 'needle two' },
      { filePath: 'README.md', lineNumber: 1, text: 'needle three' },
    ])
  })

  it('reads a folded directory chain', () => {
    const text = '# packages/pkg/src/\n## root.ts\n*1|first\n*9|second'
    expect(ohMyPiGrepResult(text, { matchCount: 2 }).lines).toEqual([
      { filePath: 'packages/pkg/src/root.ts', lineNumber: 1, text: 'first' },
      { filePath: 'packages/pkg/src/root.ts', lineNumber: 9, text: 'second' },
    ])
  })

  it('reads the `*N|text` rows of the plain modes', () => {
    // The E2E profile's `replace` edit prints no snapshot tag and a `|` after each number.
    const text = '# a.ts\n 3|context\n*4|needle\n...\n*9|needle | again'
    expect(ohMyPiGrepResult(text, { matchCount: 2 }).lines).toEqual([
      { filePath: 'a.ts', lineNumber: 4, text: 'needle' },
      { filePath: 'a.ts', lineNumber: 9, text: 'needle | again' },
    ])
  })

  it('reads a single-file scope under its hashline header', () => {
    expect(ohMyPiGrepResult('[src/a.ts#1A2B]\n*4:needle\n 5:context', { matchCount: 1, files: ['src/a.ts'] }).lines).toEqual([
      { filePath: 'src/a.ts', lineNumber: 4, text: 'needle' },
    ])
  })

  it('reads a single-file scope of the plain modes, which prints no header, from the one file omp states', () => {
    expect(ohMyPiGrepResult('*4|needle\n 5|context', { matchCount: 1, fileCount: 1, files: ['src/a.ts'] }).lines).toEqual([
      { filePath: 'src/a.ts', lineNumber: 4, text: 'needle' },
    ])
  })

  it('states no match for a row that no file header or file list places', () => {
    expect(ohMyPiGrepResult('*4|needle', { matchCount: 1, files: ['a.ts', 'b.ts'] }).lines).toEqual([])
  })

  it('states an empty result from the count, not the text', () => {
    expect(ohMyPiGrepResult('No matches found', { matchCount: 0, fileCount: 0, files: [] }).empty).toBe(true)
    expect(ohMyPiGrepResult('', {}).empty).toBe(true)
  })

  it('states a truncated result', () => {
    expect(ohMyPiGrepResult('# a#0000\n*1:x', { matchCount: 1, truncated: true }).truncated).toBe(true)
  })

  it('reads a text with Windows line endings', () => {
    expect(ohMyPiGrepResult('# src/\r\n## a.ts#1A2B\r\n*3:needle\r\n', { matchCount: 1 }).lines).toEqual([
      { filePath: 'src/a.ts', lineNumber: 3, text: 'needle' },
    ])
  })

  it('closes a directory when a header of the same level or above opens', () => {
    // `lib/` belongs under `src/`, and `docs/` closes both.
    const text = '# src/\n## lib/\n### a.ts\n*1:x\n# docs/\n## b.md\n*2:y'
    expect(ohMyPiGrepResult(text, { matchCount: 2 }).lines).toEqual([
      { filePath: 'src/lib/a.ts', lineNumber: 1, text: 'x' },
      { filePath: 'docs/b.md', lineNumber: 2, text: 'y' },
    ])
  })

  it('states no match below a directory header until a file header places it', () => {
    expect(ohMyPiGrepResult('# a.ts\n*1:x\n# src/\n*2:y', { matchCount: 2 }).lines).toEqual([{ filePath: 'a.ts', lineNumber: 1, text: 'x' }])
  })

  it('states the match count omp gives, and a count of zero as empty whatever the text', () => {
    expect(ohMyPiGrepResult('*1|x', { matchCount: 0, files: ['a.ts'] })).toMatchObject({ matchCount: 0, empty: true })
    expect(ohMyPiGrepResult('*1|x', { files: ['a.ts'] })).not.toHaveProperty('matchCount')
  })
})

describe('ohMyPiGlobResult', () => {
  it('reads the files', () => {
    expect(ohMyPiGlobResult('new.txt\nnotes.txt', { fileCount: 2, files: ['new.txt', 'notes.txt'], truncated: false })).toMatchObject({
      filenames: ['new.txt', 'notes.txt'],
      numFiles: 2,
      empty: false,
      truncated: false,
    })
  })

  it('states an empty and a capped result', () => {
    expect(ohMyPiGlobResult('', { fileCount: 0, files: [] }).empty).toBe(true)
    expect(ohMyPiGlobResult('a', { files: ['a'], resultLimitReached: true }).truncated).toBe(true)
  })

  it('counts the files it lists when omp states no count, and prefers omp\'s count to the list', () => {
    expect(ohMyPiGlobResult('a\nb', { files: ['a', 'b'] })).toMatchObject({ numFiles: 2, empty: false, truncated: false })
    // A capped list holds fewer files than omp found.
    expect(ohMyPiGlobResult('a', { files: ['a'], fileCount: 500, truncated: true })).toMatchObject({ numFiles: 500, truncated: true })
    expect(ohMyPiGlobResult('No files found', {})).toMatchObject({ filenames: [], numFiles: 0, empty: true, fallbackContent: 'No files found' })
  })
})

describe('ohMyPiTextSearchResult', () => {
  it('keeps the text as the body', () => {
    expect(ohMyPiTextSearchResult('src/a.ts: the parser', {})).toMatchObject({ content: 'src/a.ts: the parser', filenames: [], empty: false })
    expect(ohMyPiTextSearchResult('   ', {}).empty).toBe(true)
  })

  it('counts the files a result lists, and states a result with files and no text as not empty', () => {
    expect(ohMyPiTextSearchResult('', { files: ['a.ts', 'b.ts', 3], truncated: true })).toMatchObject({ filenames: ['a.ts', 'b.ts'], numFiles: 2, truncated: true, empty: false })
  })
})

describe('ohMyPiSearchPattern', () => {
  it('reads the pattern, else the query', () => {
    expect(ohMyPiSearchPattern({ pattern: 'foo($$$)' })).toBe('foo($$$)')
    expect(ohMyPiSearchPattern({ query: 'where is the parser' })).toBe('where is the parser')
    expect(ohMyPiSearchPattern({})).toBe('')
  })
})
