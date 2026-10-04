import { describe, expect, it } from 'vitest'
import { grokCommandExit, grokDirectoryEntries, grokGrepResult, grokListResult, grokRawOutput, grokStreamText } from './results'

function bytes(text: string): number[] {
  return [...new TextEncoder().encode(text)]
}

describe('grokRawOutput', () => {
  it('answers the record only under its own type', () => {
    const tool = { rawOutput: { type: 'Bash', exit_code: 0 } }
    expect(grokRawOutput(tool, 'Bash')).toEqual({ type: 'Bash', exit_code: 0 })
    expect(grokRawOutput(tool, 'ListDir')).toBeUndefined()
    expect(grokRawOutput({ rawOutput: 'text' }, 'Bash')).toBeUndefined()
    expect(grokRawOutput({}, 'Bash')).toBeUndefined()
  })
})

describe('grokStreamText', () => {
  it('decodes a byte array and keeps a string', () => {
    expect(grokStreamText(bytes('probe.txt\n'))).toBe('probe.txt\n')
    expect(grokStreamText(bytes('한'))).toBe('한')
    expect(grokStreamText('text')).toBe('text')
    expect(grokStreamText([])).toBe('')
  })

  it('answers nothing for a value that is no stream', () => {
    expect(grokStreamText([1, 'x'])).toBe('')
    expect(grokStreamText([256])).toBe('')
    expect(grokStreamText([-1])).toBe('')
    expect(grokStreamText([1.5])).toBe('')
    expect(grokStreamText(null)).toBe('')
    expect(grokStreamText({ 0: 1 })).toBe('')
  })

  it('decodes a large stream whole', () => {
    const text = 'line\n'.repeat(100_000)
    expect(grokStreamText(bytes(text))).toBe(text)
  })
})

describe('grokCommandExit', () => {
  it('reads the exit code', () => {
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: 2, signal: null } })).toEqual({ exitCode: 2 })
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: 0 } })).toEqual({ exitCode: 0 })
  })

  it('reads a signal ahead of the code Grok states beside it', () => {
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: -1, signal: 'SIGKILL' } })).toEqual({ signal: 'SIGKILL' })
  })

  it('answers nothing for another output or a record with no exit', () => {
    expect(grokCommandExit({ rawOutput: { type: 'BackgroundTaskStarted', task_id: 't' } })).toBeUndefined()
    expect(grokCommandExit({ rawOutput: { type: 'Bash' } })).toBeUndefined()
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: '2' } })).toBeUndefined()
  })

  it('reads the code when the signal is empty, and keeps a negative code', () => {
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: 1, signal: '' } })).toEqual({ exitCode: 1 })
    expect(grokCommandExit({ rawOutput: { type: 'Bash', exit_code: -1, signal: null } })).toEqual({ exitCode: -1 })
  })
})

describe('grokDirectoryEntries', () => {
  it('reads each entry relative to the root', () => {
    expect(grokDirectoryEntries('- /w/probe/\n  - hello.txt\n  - probe.txt')).toEqual([{ path: 'hello.txt' }, { path: 'probe.txt' }])
  })

  it('reads a nested directory into the paths of its entries', () => {
    expect(grokDirectoryEntries('- /w/\n  - src/\n    - a.ts\n    - lib/\n      - b.ts\n  - README.md')).toEqual([
      { path: 'src/' },
      { path: 'src/a.ts' },
      { path: 'src/lib/' },
      { path: 'src/lib/b.ts' },
      { path: 'README.md' },
    ])
  })

  it('reads an empty directory as no entries', () => {
    expect(grokDirectoryEntries('- /w/empty/\n')).toEqual([])
  })

  // An entry deeper than its parent allows states no parent, so the reader keeps its
  // name alone rather than fail or invent a directory.
  it('reads an entry that skips a level as its name alone', () => {
    expect(grokDirectoryEntries('- /w/\n      - deep.ts')).toEqual([{ path: 'deep.ts' }])
  })

  it('answers null for text that is not the tree', () => {
    expect(grokDirectoryEntries('')).toBeNull()
    expect(grokDirectoryEntries('Directory not found')).toBeNull()
    expect(grokDirectoryEntries('- /w/\nplain line')).toBeNull()
    expect(grokDirectoryEntries('- /w/\n- /second-root/')).toBeNull()
  })
})

describe('grokListResult', () => {
  it('reads the listing of a ListDir record', () => {
    expect(grokListResult({ rawOutput: { type: 'ListDir', Content: { content: '- /w/\n  - a.txt', absolute_root_path: '/w' } } })).toEqual({ entries: [{ path: 'a.txt' }] })
  })

  it('answers null for another record', () => {
    expect(grokListResult({ rawOutput: { type: 'ListDir', DirectoryNotFound: { path: '/x' } } })).toBeNull()
    expect(grokListResult({})).toBeNull()
  })
})

describe('grokGrepResult', () => {
  it('reads the grouped matches', () => {
    const result = grokGrepResult({ rawOutput: {
      type: 'GrepSearch',
      stdout: bytes('Found 2 matching lines\n/w/a.txt\n1:probe\n'),
      match_count: 2,
      file_matches: [
        { path: '/w/a.txt', matches: [{ line_number: 1, content: 'probe' }] },
        { path: '/w/b.txt', matches: [{ line_number: 4, content: 'probe again' }] },
      ],
    } })
    expect(result).toEqual({
      filenames: ['/w/a.txt', '/w/b.txt'],
      content: '',
      lines: [{ filePath: '/w/a.txt', lineNumber: 1, text: 'probe' }, { filePath: '/w/b.txt', lineNumber: 4, text: 'probe again' }],
      numFiles: 2,
      numLines: 2,
      matchCount: 2,
      truncated: false,
      fallbackContent: 'Found 2 matching lines\n/w/a.txt\n1:probe\n',
      empty: false,
    })
  })

  it('states an empty search as empty', () => {
    const result = grokGrepResult({ rawOutput: { type: 'GrepSearch', stdout: '', match_count: 0, file_matches: [] } })
    expect(result?.empty).toBe(true)
    expect(result?.numFiles).toBe(0)
  })

  it('counts the matches when Grok states no count', () => {
    expect(grokGrepResult({ rawOutput: { type: 'GrepSearch', file_matches: [{ path: '/a', matches: [{ content: 'x' }, { line_number: 2, content: 'y' }] }] } })?.matchCount).toBe(2)
  })

  it('names each file once, skips an entry that is no object, and states no line number that Grok omits', () => {
    const result = grokGrepResult({ rawOutput: {
      type: 'GrepSearch',
      file_matches: [
        { path: '/a', matches: [{ line_number: 1, content: 'x' }] },
        'noise',
        { path: '/a', matches: [{ content: 'y' }, 7] },
        { path: '', matches: [{ line_number: 2, content: 'z' }] },
        { path: '/b', matches: 'none' },
      ],
    } })
    expect(result?.filenames).toEqual(['/a'])
    expect(result?.numFiles).toBe(1)
    expect(result?.lines).toEqual([{ filePath: '/a', lineNumber: 1, text: 'x' }, { filePath: '/a', text: 'y' }, { filePath: '', lineNumber: 2, text: 'z' }])
    expect(result?.matchCount).toBe(3)
  })

  // Grok's own count wins: it counts the matches it found, and the file groups can
  // hold fewer when Grok cut the list.
  it('keeps the count that Grok states over the lines it grouped', () => {
    expect(grokGrepResult({ rawOutput: { type: 'GrepSearch', match_count: 250, file_matches: [{ path: '/a', matches: [{ content: 'x' }] }] } })?.matchCount).toBe(250)
  })

  it('answers null for a record without the grouped matches', () => {
    expect(grokGrepResult({ rawOutput: { type: 'GrepSearch', stdout: [] } })).toBeNull()
    expect(grokGrepResult({ rawOutput: { type: 'Bash' } })).toBeNull()
  })
})
