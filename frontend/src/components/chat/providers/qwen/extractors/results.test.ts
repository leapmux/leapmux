import { describe, expect, it } from 'vitest'
import { qwenGlobResult, qwenGrepResult, qwenListResult, qwenReadResult } from './results'

describe('qwenReadResult', () => {
  it('numbers a whole file from its first line', () => {
    expect(qwenReadResult('one\ntwo\n', null).lines).toEqual([{ num: 1, text: 'one' }, { num: 2, text: 'two' }])
  })

  it('numbers a read from its 0-based offset', () => {
    expect(qwenReadResult('ten\n', 9).lines).toEqual([{ num: 10, text: 'ten' }])
  })

  it('reads the first line of a truncated read from Qwen\'s notice, and keeps the notice', () => {
    const result = qwenReadResult('Showing lines 5-6 of at least 90 total lines.\n\n---\n\nfive\nsix', null)
    expect(result.lines).toEqual([{ num: 5, text: 'five' }, { num: 6, text: 'six' }])
    expect(result.leading).toEqual([{ label: 'Partial view', text: 'Showing lines 5-6 of at least 90 total lines.' }])
  })

  it('reads an empty file as zero lines', () => {
    expect(qwenReadResult('', null).lines).toEqual([])
  })

  it('numbers a read at offset zero from the first line', () => {
    expect(qwenReadResult('one\n', 0).lines).toEqual([{ num: 1, text: 'one' }])
  })

  // The notice states the first line it shows, which is the truth when the
  // arguments state an offset too.
  it('numbers a truncated read from the notice over the offset', () => {
    const result = qwenReadResult('Showing lines 21-22 of 40 total lines.\n\n---\n\ntwenty-one\ntwenty-two\n', 3)
    expect(result.lines).toEqual([{ num: 21, text: 'twenty-one' }, { num: 22, text: 'twenty-two' }])
    expect(result.fallbackContent).toBe('Showing lines 21-22 of 40 total lines.\n\n---\n\ntwenty-one\ntwenty-two\n')
  })

  // One trailing newline ends the last line. A second one is a blank line of the file.
  it('keeps a blank last line of the file', () => {
    expect(qwenReadResult('one\n\n', null).lines).toEqual([{ num: 1, text: 'one' }, { num: 2, text: '' }])
  })

  it('reads a notice-like line inside the file as file text', () => {
    const text = 'intro\nShowing lines 1-2 of 3 total lines.\n\n---\n\n'
    expect(qwenReadResult(text, null).leading).toBeUndefined()
    expect(qwenReadResult(text, null).lines?.[0]).toEqual({ num: 1, text: 'intro' })
  })
})

describe('qwenGrepResult', () => {
  it('reads the matches grouped by file', () => {
    const result = qwenGrepResult('Found 3 matches for pattern "needle" in path "/p":\n---\nFile: a.ts\nL3: needle\nL9: needle again\n---\nFile: sub/b.ts\nL1: needle: with a colon\n---\n')
    expect(result?.lines).toEqual([
      { filePath: 'a.ts', lineNumber: 3, text: 'needle' },
      { filePath: 'a.ts', lineNumber: 9, text: 'needle again' },
      { filePath: 'sub/b.ts', lineNumber: 1, text: 'needle: with a colon' },
    ])
    expect(result).toMatchObject({ filenames: ['a.ts', 'sub/b.ts'], numFiles: 2, numLines: 3, matchCount: 3, truncated: false, empty: false })
  })

  it('states a truncated search', () => {
    expect(qwenGrepResult('Found 9 matches for pattern "x" in path ".":\n---\nFile: a\nL1: x\n--- [8 lines truncated] ...')?.truncated).toBe(true)
  })

  it('reads the sentence of a search that found nothing as empty', () => {
    expect(qwenGrepResult('No matches found for pattern "x" in path ".".')).toMatchObject({ empty: true, numFiles: 0, matchCount: 0 })
  })

  it('reads the header of a search that found one match', () => {
    expect(qwenGrepResult('Found 1 match for pattern "x" in path ".":\n---\nFile: a.ts\nL2: x\n---\n')).toMatchObject({ filenames: ['a.ts'], matchCount: 1, numLines: 1 })
  })

  // A match line belongs to the file before it. One with no file before it has no
  // file to draw it under, so it states nothing.
  it('drops a match line that comes before any file line', () => {
    expect(qwenGrepResult('Found 2 matches for pattern "x" in path ".":\n---\nL1: orphan\nFile: a.ts\nL2: x\n---\n')?.lines).toEqual([{ filePath: 'a.ts', lineNumber: 2, text: 'x' }])
  })

  // The header states the count Qwen found, which can be more than the lines it
  // printed before it truncated the output.
  it('keeps the count of the header beside fewer printed lines', () => {
    const result = qwenGrepResult('Found 50 matches for pattern "x" in path ".":\n---\nFile: a.ts\nL1: x\n...')
    expect(result).toMatchObject({ matchCount: 50, numLines: 1, truncated: true })
  })

  it('answers null for any other text', () => {
    expect(qwenGrepResult('grep failed')).toBeNull()
  })
})

describe('qwenGlobResult', () => {
  it('reads the files newest first', () => {
    const result = qwenGlobResult('Found 2 file(s) matching "*.ts" within /p, sorted by modification time (newest first):\n---\n/p/b.ts\n/p/a.ts')
    expect(result).toMatchObject({ filenames: ['/p/b.ts', '/p/a.ts'], numFiles: 2, truncated: false, empty: false })
  })

  it('keeps the truncation notice', () => {
    const result = qwenGlobResult('Found at least 900 file(s) matching "**" within /p, sorted by modification time (newest first):\n---\n/p/a\n---\n[Results truncated after scanning 900 matching files. Narrow the pattern or path.]')
    expect(result).toMatchObject({ filenames: ['/p/a'], truncated: true, notice: '[Results truncated after scanning 900 matching files. Narrow the pattern or path.]' })
  })

  it('reads a search that found nothing as empty, and other text as unreadable', () => {
    expect(qwenGlobResult('No files found matching pattern "*.x" within /p')?.empty).toBe(true)
    expect(qwenGlobResult('Error: bad pattern')).toBeNull()
  })

  it('drops blank lines between the files', () => {
    expect(qwenGlobResult('Found 2 file(s) matching "*" within /p, sorted by modification time (newest first):\n---\n/p/a\n\n  \n/p/b\n')?.filenames).toEqual(['/p/a', '/p/b'])
  })
})

describe('qwenListResult', () => {
  it('reads each entry and marks a directory with a slash', () => {
    expect(qwenListResult('Listed 2 item(s) in /p:\n---\n[DIR] src\na.ts')).toEqual({ entries: [{ path: 'src/' }, { path: 'a.ts' }], totalEntries: 2 })
  })

  it('states a truncated listing and its notices', () => {
    expect(qwenListResult('Listed 3 item(s) in /p:\n---\na\n---\n[2 items truncated] ...\n\n(1 git-ignored)')).toEqual({
      entries: [{ path: 'a' }],
      totalEntries: 3,
      truncated: true,
      notice: '[2 items truncated] ...\n(1 git-ignored)',
    })
  })

  it('answers null for any other text', () => {
    expect(qwenListResult('Error listing directory')).toBeNull()
  })

  it('reads a listing of no entries as whole', () => {
    expect(qwenListResult('Listed 0 item(s) in /p:\n---\n')).toEqual({ entries: [], totalEntries: 0 })
  })

  // `[DIR]` marks a directory only as the first word of its line.
  it('reads a name that holds the directory mark later in it as a file', () => {
    expect(qwenListResult('Listed 1 item(s) in /p:\n---\nnotes [DIR] old.txt')).toEqual({ entries: [{ path: 'notes [DIR] old.txt' }], totalEntries: 1 })
  })
})
