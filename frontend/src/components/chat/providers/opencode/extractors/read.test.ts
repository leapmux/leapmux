import { describe, expect, it } from 'vitest'
import { openCodeReadResult } from './read'

describe('openCodeReadResult', () => {
  it('reads a native page and preserves a selected blank line', () => {
    expect(openCodeReadResult('Read file /file, lines 4-5\n4: text\n5: \n[Output truncated. Continue reading with offset: 6]')).toEqual({
      lines: [
        { num: 4, text: 'text' },
        { num: 5, text: '' },
      ],
      fallbackContent: 'text\n',
    })
  })

  it('recognizes an empty file and a native past-end tail', () => {
    expect(openCodeReadResult('Read file /file, 0 lines')).toEqual({ lines: [], fallbackContent: '' })
    expect(openCodeReadResult('Read file /file, lines 2-2 (offset 90 is past the end; the file has 2 lines)\n2: tail')?.lines).toEqual([
      { num: 2, text: 'tail' },
    ])
  })

  it.each([
    'unrecognized output',
    'Read file /file, lines 0-0\n0: invalid',
    'Read file /file, lines 2-1',
    'Read file /file, lines 1-2\n1: only one',
    'Read file /file, lines 1-1\n2: wrong row',
    'Read file /file, lines 9007199254740992-9007199254740992\n9007199254740992: unsafe',
  ])('preserves an unrecognized range as unparsed output: %s', (text) => {
    expect(openCodeReadResult(text)).toBeNull()
  })
})
