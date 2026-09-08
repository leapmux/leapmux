import type { IBufferRange } from '@xterm/xterm'
import { Terminal } from '@xterm/xterm'

/**
 * A headless terminal holding `text`, with every write already applied to the
 * buffer.
 *
 * `Terminal.write` is asynchronous even without `open()`, so a test that reads
 * the buffer straight after it sees the previous state. The callback is the
 * only signal that the parser drained.
 */
export async function terminalWith(text: string, cols = 40): Promise<Terminal> {
  const terminal = new Terminal({ cols, rows: 8 })
  await new Promise<void>(resolve => terminal.write(text, resolve))
  return terminal
}

/** The range xterm reports for a link: 1-based, and `end.x` is inclusive. */
export function linkRange(row: number, startColumn: number, endColumn: number): IBufferRange {
  return { start: { x: startColumn, y: row }, end: { x: endColumn, y: row } }
}
