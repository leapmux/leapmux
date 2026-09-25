import { describe, expect, it } from 'vitest'
import { codewhaleReadRequest, codewhaleReadResult } from './read'

describe('codewhaleReadRequest', () => {
  it('reads the path and the window', () => {
    expect(codewhaleReadRequest({ path: 'a.ts', offset: 3, limit: 5 })).toStrictEqual({ path: 'a.ts', offset: 3, limit: 5 })
  })

  it('reads a handle and a stored reference as what the call addresses', () => {
    expect(codewhaleReadRequest({ handle: { kind: 'var_handle', session_id: 's1', name: 'rows' } })).toStrictEqual({ path: 's1/rows' })
    expect(codewhaleReadRequest({ handle: { name: 'rows' } })).toStrictEqual({ path: 'rows' })
    expect(codewhaleReadRequest({ handle: 's1/rows' })).toStrictEqual({ path: 's1/rows' })
    expect(codewhaleReadRequest({ ref: 'art_1' })).toStrictEqual({ path: 'art_1' })
    expect(codewhaleReadRequest({})).toStrictEqual({ path: '' })
  })

  it('reads a stored result by its id, and a handle before any other reference', () => {
    expect(codewhaleReadRequest({ id: 'res_1' })).toStrictEqual({ path: 'res_1' })
    expect(codewhaleReadRequest({ handle: 's1/rows', ref: 'art_1', id: 'res_1' })).toStrictEqual({ path: 's1/rows' })
    expect(codewhaleReadRequest({ path: 'a.ts', handle: 's1/rows' })).toStrictEqual({ path: 'a.ts' })
  })

  // A handle with a session and no name addresses nothing the runtime can read, so
  // the next reference answers.
  it('reads past a handle that states no name', () => {
    expect(codewhaleReadRequest({ handle: { session_id: 's1' }, ref: 'art_1' })).toStrictEqual({ path: 'art_1' })
    expect(codewhaleReadRequest({ handle: 42 })).toStrictEqual({ path: '' })
  })

  it('reads a window that states only a limit, and leaves out an offset or a limit in another shape', () => {
    expect(codewhaleReadRequest({ path: 'a.ts', limit: 20 })).toStrictEqual({ path: 'a.ts', limit: 20 })
    expect(codewhaleReadRequest({ path: 'a.ts', offset: '3', limit: null })).toStrictEqual({ path: 'a.ts' })
  })
})

describe('codewhaleReadResult', () => {
  it('numbers the lines from the offset', () => {
    expect(codewhaleReadResult('a\nb', { path: 'x', offset: 7 })).toStrictEqual({ lines: [{ num: 7, text: 'a' }, { num: 8, text: 'b' }], fallbackContent: 'a\nb' })
  })

  it('numbers from one for an absent, zero, negative, fractional or unsafe offset', () => {
    for (const offset of [undefined, 0, -3, 2.5, Number.MAX_SAFE_INTEGER + 2]) {
      const request = offset === undefined ? { path: 'x' } : { path: 'x', offset }
      expect(codewhaleReadResult('a', request).lines).toStrictEqual([{ num: 1, text: 'a' }])
    }
  })

  it('splits the paging notice off the file', () => {
    const notice = '[Showing lines 1-2 of 90 (1.2 KB total, 100000-byte output budget). Use offset=3 to continue.]'
    expect(codewhaleReadResult(`a\nb\n\n${notice}`, { path: 'x' })).toStrictEqual({
      lines: [{ num: 1, text: 'a' }, { num: 2, text: 'b' }],
      fallbackContent: 'a\nb',
      trailing: [{ label: 'Notice', text: notice }],
    })
    const more = '[12 more lines in file (1 KB total). Use offset=41 to continue.]'
    expect(codewhaleReadResult(`a\n\n${more}`, { path: 'x' }).trailing).toStrictEqual([{ label: 'Notice', text: more }])
  })

  it('keeps a bracketed line the file itself holds', () => {
    expect(codewhaleReadResult('a\n\n[not a notice]', { path: 'x' }).trailing).toBeUndefined()
  })

  // The runtime puts a blank line before its notice, and only at the end. A line in
  // the notice's words anywhere else is the file's own.
  it('keeps a notice-shaped line with no blank line before it, or with text after it', () => {
    const notice = '[Showing lines 1-2 of 90 (1 KB total). Use offset=3 to continue.]'
    expect(codewhaleReadResult(`a\n${notice}`, { path: 'x' }).trailing).toBeUndefined()
    expect(codewhaleReadResult(`a\n\n${notice}\nb`, { path: 'x' }).trailing).toBeUndefined()
  })

  it('numbers a window from its offset and keeps the notice apart', () => {
    const notice = '[Showing lines 41-42 of 90 (1 KB total). Use offset=43 to continue.]'
    expect(codewhaleReadResult(`forty-one\nforty-two\n\n${notice}`, { path: 'x', offset: 41 })).toStrictEqual({
      lines: [{ num: 41, text: 'forty-one' }, { num: 42, text: 'forty-two' }],
      fallbackContent: 'forty-one\nforty-two',
      trailing: [{ label: 'Notice', text: notice }],
    })
  })

  it('states an empty file as zero lines', () => {
    expect(codewhaleReadResult('', { path: 'x' })).toStrictEqual({ lines: [], fallbackContent: '' })
  })
})
