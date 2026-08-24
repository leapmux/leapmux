import { describe, expect, it } from 'vitest'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'

// The two source-text guards read their whole verdict through these, so a hole
// here reads as a clean suite rather than as a failure. Pin the openers each
// one blanks, the line numbering it must preserve, and the boundaries.

describe('stripCommentLines', () => {
  it('blanks every line that opens or continues a comment', () => {
    const source = [
      'const a = 1',
      '// a line comment',
      '  /* a block opener',
      '   * a block body',
      '   */',
      'const b = 2',
    ].join('\n')

    expect(stripCommentLines(source).split('\n')).toEqual([
      'const a = 1',
      '',
      '',
      '',
      '',
      'const b = 2',
    ])
  })

  it('keeps a code line that carries a trailing comment', () => {
    const source = 'const a = 1 // still code'
    expect(stripCommentLines(source)).toBe(source)
  })

  it('keeps the line count, so a scan reports the original line number', () => {
    const code = stripCommentLines(['// one', 'code', '// three', 'code'].join('\n'))
    expect(code.split('\n')).toHaveLength(4)
    expect(lineNumberAt(code, code.indexOf('code'))).toBe(2)
  })

  it('leaves an empty source empty', () => {
    expect(stripCommentLines('')).toBe('')
  })
})

describe('lineNumberAt', () => {
  const source = ['first', 'second', 'third'].join('\n')

  it('numbers from one', () => {
    expect(lineNumberAt(source, 0)).toBe(1)
  })

  it('keeps a newline on the line it ends', () => {
    // The index of the `\n` after `first` is still line 1; the character after
    // it opens line 2.
    expect(lineNumberAt(source, source.indexOf('\n'))).toBe(1)
    expect(lineNumberAt(source, source.indexOf('\n') + 1)).toBe(2)
  })

  it('finds a later line', () => {
    expect(lineNumberAt(source, source.indexOf('third'))).toBe(3)
  })

  it('reports the last line for an index past the end', () => {
    expect(lineNumberAt(source, source.length + 100)).toBe(3)
  })

  it('reports the first line for an index below the start', () => {
    // A caller that subtracts an offset past zero gets line 1, never 0 and
    // never a crash.
    expect(lineNumberAt(source, -5)).toBe(1)
  })

  it('reports the first line of an empty source', () => {
    expect(lineNumberAt('', 0)).toBe(1)
  })
})
