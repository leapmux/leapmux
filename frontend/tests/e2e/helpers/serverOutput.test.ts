/** Test process output, partial lines, and absolute log marks without a Hub. */
import { Buffer } from 'node:buffer'
import { ChildProcess } from 'node:child_process'
import { PassThrough } from 'node:stream'
import { describe, expect, it } from 'vitest'
import { createServerOutput } from './serverOutput'

/** Supply controlled streams on a real ChildProcess instance. */
function fakeProc(): ChildProcess & { write: (s: string) => void, close: () => void } {
  const stdout = new PassThrough()
  const stderr = new PassThrough()
  const proc = new ChildProcess()
  return Object.assign(proc, {
    stdout,
    stderr,
    write: (s: string) => stderr.emit('data', Buffer.from(s)),
    close: () => { proc.emit('close', 0, null) },
  })
}

describe('createServerOutput', () => {
  it('keeps stdout and stderr fragments from one process separate', () => {
    const output = createServerOutput()
    const proc = fakeProc()
    output.capture(proc, 'worker')
    proc.stdout?.emit('data', Buffer.from('stdout-'))
    proc.write('stderr-')
    proc.stdout?.emit('data', Buffer.from('complete\n'))
    proc.write('complete\n')
    expect(output.since(0)).toBe('[worker] stdout-complete\n[worker] stderr-complete')
  })

  it('keeps a UTF-8 character split across output chunks intact', () => {
    const output = createServerOutput()
    const proc = fakeProc()
    output.capture(proc, 'worker')
    const line = Buffer.from('start \u{1F6A7} end\n')
    proc.stdout?.emit('data', line.subarray(0, 7))
    proc.stdout?.emit('data', line.subarray(7))
    expect(output.since(0)).toBe('[worker] start \u{1F6A7} end')
  })

  it('labels each line and joins chunks that split one', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'worker')

    proc.write('first\nsec')
    proc.write('ond\n')

    expect(out.since(0)).toBe('[worker] first\n[worker] second')
  })

  it('omits the prefix when no label is given', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc)

    proc.write('bare\n')

    expect(out.since(0)).toBe('bare')
  })

  it('slices from a mark, so one test never reads another test\'s output', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'hub')

    proc.write('before\n')
    const mark = out.mark()
    proc.write('after\n')

    expect(out.since(mark)).toBe('[hub] after')
    expect(out.since(0)).toBe('[hub] before\n[hub] after')
  })

  it('carries a line each process has not terminated yet', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'hub')

    // A process can stop during a panic without writing a newline. Keep that partial line for diagnostics.
    proc.write('panic: nil map')

    expect(out.since(0)).toBe('[hub] panic: nil map')
  })

  it('keeps two processes\' half-lines apart', () => {
    const out = createServerOutput()
    const hub = fakeProc()
    const worker = fakeProc()
    out.capture(hub, 'hub')
    out.capture(worker, 'worker')

    // The two processes write interleaved fragments. A shared partial line would combine those fragments and corrupt both lines.
    hub.write('hub-')
    worker.write('worker-')
    hub.write('half\n')
    worker.write('half\n')

    expect(out.since(0)).toBe('[hub] hub-half\n[worker] worker-half')
  })

  it('spans a restart and lands the dead process\'s last line once', () => {
    const out = createServerOutput()
    const first = fakeProc()
    out.capture(first, 'worker')

    first.write('serving\n')
    first.write('shutting down')
    first.close()

    const second = fakeProc()
    out.capture(second, 'worker')
    second.write('connected to hub\n')

    const expected = '[worker] serving\n[worker] shutting down\n[worker] connected to hub'
    expect(out.since(0)).toBe(expected)
    // Read twice. If close leaves the partial line, each later read repeats that line.
    expect(out.since(0)).toBe(expected)
  })

  it('drops the oldest lines and keeps a mark taken after them accurate', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'worker')

    // Write more lines than the buffer can retain.
    for (let i = 0; i < 5000; i++)
      proc.write(`line-${i}\n`)

    const kept = out.since(0).split('\n')
    expect(kept.length, 'the ring has a maximum size').toBeLessThan(5000)
    expect(kept.at(-1), 'the newest line survives').toBe('[worker] line-4999')
    expect(kept[0], 'the oldest lines were evicted').not.toBe('[worker] line-0')

    // Removed lines do not change the absolute mark. Read the lines after that mark.
    const mark = out.mark()
    proc.write('after-the-mark\n')
    expect(out.since(mark)).toBe('[worker] after-the-mark')
  })

  it('returns everything still buffered for a mark the ring has passed', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'worker')

    const mark = out.mark()
    for (let i = 0; i < 5000; i++)
      proc.write(`line-${i}\n`)

    // The mark precedes the retained lines. Return the retained lines without throwing or returning an empty string.
    const slice = out.since(mark).split('\n')
    expect(slice.at(-1)).toBe('[worker] line-4999')
    expect(slice.length).toBeGreaterThan(100)
  })
})
