/**
 * Unit tests for the E2E fixtures' server-output ring buffer.
 *
 * The module under test lives at `tests/e2e/helpers/serverOutput.ts`, not beside
 * this file, and it cannot carry its own `.test.ts`: `vitest.config.ts` excludes
 * `tests/e2e/**` so Playwright specs are never collected as unit tests, and that
 * exclusion covers the helpers too. A test placed there simply never runs.
 * `visibleChatLocators.test.ts` reaches across the same boundary for the same
 * reason.
 */
import type { ChildProcess } from 'node:child_process'
import { Buffer } from 'node:buffer'
import { EventEmitter } from 'node:events'
import { describe, expect, it } from 'vitest'
import { createServerOutput } from '../../tests/e2e/helpers/serverOutput'

/** A child process with just the surface `capture` touches. */
function fakeProc(): ChildProcess & { write: (s: string) => void, close: () => void } {
  const stdout = new EventEmitter()
  const stderr = new EventEmitter()
  const proc = new EventEmitter() as unknown as ChildProcess & {
    write: (s: string) => void
    close: () => void
  }
  Object.assign(proc, {
    stdout,
    stderr,
    write: (s: string) => stderr.emit('data', Buffer.from(s)),
    close: () => (proc as unknown as EventEmitter).emit('close'),
  })
  return proc
}

describe('createServerOutput', () => {
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

    // A panic that stopped the process mid-write is exactly the line worth
    // reading, and it arrives with no trailing newline.
    proc.write('panic: nil map')

    expect(out.since(0)).toBe('[hub] panic: nil map')
  })

  it('keeps two processes\' half-lines apart', () => {
    const out = createServerOutput()
    const hub = fakeProc()
    const worker = fakeProc()
    out.capture(hub, 'hub')
    out.capture(worker, 'worker')

    // Interleaved mid-line. One shared carry would splice "hub-" onto
    // "worker-half" and lose both lines.
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
    // Read twice: a carry the close did not reap would be appended to every
    // later slice, so the dying fragment would repeat after every restart.
    expect(out.since(0)).toBe(expected)
  })

  it('drops the oldest lines and keeps a mark taken after them accurate', () => {
    const out = createServerOutput()
    const proc = fakeProc()
    out.capture(proc, 'worker')

    // Comfortably past the ring's capacity.
    for (let i = 0; i < 5000; i++)
      proc.write(`line-${i}\n`)

    const kept = out.since(0).split('\n')
    expect(kept.length, 'the ring is bounded').toBeLessThan(5000)
    expect(kept.at(-1), 'the newest line survives').toBe('[worker] line-4999')
    expect(kept[0], 'the oldest lines were evicted').not.toBe('[worker] line-0')

    // A mark taken now must still slice exactly, although the ring has already
    // evicted lines: `mark` is an absolute index, not an offset into the array.
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

    // The mark predates lines the ring has since dropped. It must answer with
    // what remains rather than throw or answer empty.
    const slice = out.since(mark).split('\n')
    expect(slice.at(-1)).toBe('[worker] line-4999')
    expect(slice.length).toBeGreaterThan(100)
  })
})
