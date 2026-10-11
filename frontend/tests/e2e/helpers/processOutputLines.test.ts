import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { createProcessOutputLineDecoder } from './processOutputLines'

describe('createProcessOutputLineDecoder', () => {
  it('joins split JSON and keeps complete lines in order', () => {
    const lines: string[] = []
    const decoder = createProcessOutputLineDecoder(line => lines.push(line))
    decoder.write('{"phase":')
    expect(lines).toEqual([])
    expect(decoder.partial()).toBe('{"phase":')
    decoder.write('"ready"}\nnext\npartial')
    expect(lines).toEqual(['{"phase":"ready"}', 'next'])
    expect(decoder.partial()).toBe('partial')
    decoder.end()
    expect(lines).toEqual(['{"phase":"ready"}', 'next', 'partial'])
    expect(decoder.partial()).toBe('')
  })

  it('preserves a UTF-8 character split at every byte boundary', () => {
    const source = Buffer.from('before \u{1F6A7} after\n')
    for (let boundary = 1; boundary < source.length; boundary++) {
      const lines: string[] = []
      const decoder = createProcessOutputLineDecoder(line => lines.push(line))
      decoder.write(source.subarray(0, boundary))
      decoder.write(source.subarray(boundary))
      decoder.end()
      expect(lines).toEqual(['before \u{1F6A7} after'])
    }
  })

  it('normalizes CRLF across chunk boundaries and preserves empty complete lines', () => {
    const lines: string[] = []
    const decoder = createProcessOutputLineDecoder(line => lines.push(line))
    decoder.write('first\r')
    decoder.write('\n\r\nsecond\n')
    decoder.end()
    expect(lines).toEqual(['first', '', 'second'])
  })

  it('flushes a final line once and keeps an empty stream empty', () => {
    const lines: string[] = []
    const decoder = createProcessOutputLineDecoder(line => lines.push(line))
    decoder.write('last line')
    decoder.end()
    decoder.end()
    expect(lines).toEqual(['last line'])
    const empty = createProcessOutputLineDecoder(line => lines.push(line))
    empty.write(Buffer.alloc(0))
    empty.end()
    expect(lines).toEqual(['last line'])
  })

  it('keeps a large line complete and refuses a write after stream end', () => {
    const lines: string[] = []
    const decoder = createProcessOutputLineDecoder(line => lines.push(line))
    const large = 'x'.repeat(1_000_000)
    decoder.write(large.slice(0, 500_000))
    decoder.write(`${large.slice(500_000)}\n`)
    decoder.end()
    expect(lines).toEqual([large])
    expect(() => decoder.write('late output')).toThrow('stream already ended')
  })
})
