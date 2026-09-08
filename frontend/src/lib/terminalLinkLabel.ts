import type { IBufferRange, Terminal } from '@xterm/xterm'
import type { UntrustedLinkLabel } from './untrustedLinks'

/**
 * Read the text a link shows, and where that text sits in its logical line.
 *
 * Returns null when the row is gone -- scrollback drops the oldest rows, so a
 * busy terminal can retire the clicked line between the click and this read.
 */
export function readTerminalLinkLabel(terminal: Terminal, range: IBufferRange): UntrustedLinkLabel | null {
  const buffer = terminal.buffer.active
  // `IBufferRange` positions are 1-based, and their `y` counts whole buffer
  // rows (scrollback included), which is the index `getLine` takes once it is
  // made 0-based. See xterm's `Linkifier._positionFromMouseEvent`, which adds
  // `buffer.ydisp` to the viewport row before it builds the range.
  const clickedRow = range.start.y - 1
  const clicked = buffer.getLine(clickedRow)
  if (!clicked)
    return null

  // A wrapped line is ONE logical line spread over several buffer rows, and
  // xterm reports one link range per row -- so the clicked range holds only a
  // fragment of the label whenever the link crosses a row edge. Walk to both
  // ends of the wrap group, so the comparison below can see the whole label.
  let firstRow = clickedRow
  while (firstRow > 0 && buffer.getLine(firstRow)?.isWrapped)
    firstRow--
  let lastRow = clickedRow
  while (lastRow + 1 < buffer.length && buffer.getLine(lastRow + 1)?.isWrapped)
    lastRow++

  let logicalLine = ''
  let rowStart = 0
  let rowEnd = 0
  for (let row = firstRow; row <= lastRow; row++) {
    const line = buffer.getLine(row)
    if (!line)
      continue
    if (row === clickedRow)
      rowStart = logicalLine.length
    // Untrimmed, so a column keeps its offset: trimming a row would shorten
    // the prefix that every later row's offset is measured from. A wide (CJK)
    // cell still yields one character for two columns, which is why the
    // offsets below come from `translateToString` too and never from column
    // arithmetic.
    logicalLine += line.translateToString(false)
    if (row === clickedRow)
      rowEnd = logicalLine.length
  }

  const label = clicked.translateToString(false, range.start.x - 1, range.end.x)
  const labelStart = rowStart + clicked.translateToString(false, 0, range.start.x - 1).length
  return { label, logicalLine, labelStart, labelEnd: labelStart + label.length, rowStart, rowEnd }
}
