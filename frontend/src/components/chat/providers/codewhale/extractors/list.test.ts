import { describe, expect, it } from 'vitest'
import { codewhaleListResult } from './list'

describe('codewhaleListResult', () => {
  it('marks each directory with a trailing slash', () => {
    expect(codewhaleListResult(JSON.stringify([{ name: 'src', is_dir: true }, { name: 'a.ts', is_dir: false }, { is_dir: true }])))
      .toStrictEqual({ entries: [{ path: 'src/' }, { path: 'a.ts' }] })
  })

  it('states the total and the cut of a capped directory', () => {
    expect(codewhaleListResult(JSON.stringify({ entries: [{ name: 'a' }], listed_entries: 1, total_entries: 9, truncated: true })))
      .toStrictEqual({ entries: [{ path: 'a' }], totalEntries: 9, truncated: true })
  })

  it('reads a project map as its key files under its summary', () => {
    expect(codewhaleListResult(JSON.stringify({ tree: 'src/', summary: ' A project ', key_files: ['package.json', ''] })))
      .toStrictEqual({ entries: [{ path: 'package.json' }], notice: 'A project' })
    expect(codewhaleListResult(JSON.stringify({ tree: 'src/', summary: '', key_files: [] }))).toStrictEqual({ entries: [] })
  })

  it('answers null for text that is not a listing', () => {
    expect(codewhaleListResult('Permission denied')).toBeNull()
    expect(codewhaleListResult('{"a":1}')).toBeNull()
    expect(codewhaleListResult('null')).toBeNull()
    expect(codewhaleListResult('')).toBeNull()
    expect(codewhaleListResult(JSON.stringify({ entries: 'a' }))).toBeNull()
  })

  it('states an empty directory as no entries', () => {
    expect(codewhaleListResult('[]')).toStrictEqual({ entries: [] })
  })

  // Only a cap the tool states is a cap: an absent total states no total, and a
  // false flag states no cut.
  it('states no total and no cut for a capped listing that reports neither', () => {
    expect(codewhaleListResult(JSON.stringify({ entries: [{ name: 'a' }], truncated: false }))).toStrictEqual({ entries: [{ path: 'a' }] })
    expect(codewhaleListResult(JSON.stringify({ entries: [], total_entries: 0 }))).toStrictEqual({ entries: [], totalEntries: 0 })
  })

  it('reads only a boolean true as a directory', () => {
    expect(codewhaleListResult(JSON.stringify([{ name: 'src', is_dir: 'true' }, 'x', { name: 'lib', is_dir: 1 }]))).toStrictEqual({ entries: [{ path: 'src' }, { path: 'lib' }] })
  })
})
