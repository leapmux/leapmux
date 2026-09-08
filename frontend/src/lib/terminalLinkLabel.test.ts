import { describe, expect, it } from 'vitest'
import { linkRange, terminalWith } from '~/test-support/xtermBuffer'
import { readTerminalLinkLabel } from './terminalLinkLabel'

describe('readTerminalLinkLabel', () => {
  it('reads the cells the range covers, and where they sit in the row', async () => {
    const terminal = await terminalWith('go to example now')

    const placement = readTerminalLinkLabel(terminal, linkRange(1, 7, 13))

    expect(placement?.label).toBe('example')
    expect(placement?.labelStart).toBe(6)
    expect(placement?.labelEnd).toBe(13)
    expect(placement?.rowStart).toBe(0)
    // The row is joined untrimmed, so a column keeps its offset.
    expect(placement?.rowEnd).toBe(40)
  })

  it('joins every row of a wrapped line, so a fragment can be placed in it', async () => {
    // 20 columns, so the address below occupies the first row and part of the
    // second — which is what makes xterm report two ranges for one link.
    const terminal = await terminalWith('https://example.test/abcdefgh', 20)

    const secondRow = readTerminalLinkLabel(terminal, linkRange(2, 1, 9))

    expect(secondRow?.label).toBe('/abcdefgh')
    expect(secondRow?.logicalLine.startsWith('https://example.test/abcdefgh')).toBe(true)
    expect(secondRow?.rowStart).toBe(20)
    expect(secondRow?.labelStart).toBe(20)
  })

  it('measures a label in CHARACTERS, past text that is two columns wide', async () => {
    // A wide (CJK) cell fills two columns and yields one character, so column
    // arithmetic and string offsets diverge the moment such text precedes a
    // link. Getting this wrong slices the label and prompts over an address
    // that matches -- the loudest possible false alarm.
    const terminal = await terminalWith('\u65E5\u672C\u8A9E https://example.test/x')

    // Columns 8..29 hold the address: three wide cells and a space take 7.
    const placement = readTerminalLinkLabel(terminal, linkRange(1, 8, 29))

    expect(placement?.label).toBe('https://example.test/x')
    // Four characters precede it, not seven columns.
    expect(placement?.labelStart).toBe(4)
  })

  it('returns null when the clicked row already left the buffer', async () => {
    const terminal = await terminalWith('one line')

    expect(readTerminalLinkLabel(terminal, linkRange(999, 1, 3))).toBeNull()
  })
})
