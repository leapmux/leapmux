import type { TerminalInstance } from '~/lib/terminal'
import { describe, expect, it, vi } from 'vitest'
import { applyTerminalData } from '~/lib/terminal'

// A bare minimum stub that exercises the two xterm.Terminal methods
// applyTerminalData touches (write, reset), records the call order, and
// preserves the onParsed callback semantics so onParsed side effects can
// be asserted. Intentionally NOT a real xterm — the contract we care
// about is "reset before write on snapshot, write only on incremental".
function makeStubInstance(): TerminalInstance & { _log: string[] } {
  const log: string[] = []
  return {
    terminal: {
      reset: vi.fn(() => { log.push('reset') }),
      write: vi.fn((data: Uint8Array, cb?: () => void) => {
        log.push(`write:${new TextDecoder().decode(data)}`)
        cb?.()
      }),
    } as any,
    fitAddon: { fit: vi.fn() } as any,
    suppressInput: false,
    dispose: vi.fn(),
    _log: log,
  }
}

describe('applyTerminalData', () => {
  it('is_snapshot=true: resets xterm before writing the payload', () => {
    const inst = makeStubInstance()
    applyTerminalData(inst, new TextEncoder().encode('snap'), true, 4, 0)

    // The reset MUST come before the write. An implementation that
    // writes first and then resets would still pass "both were called"
    // but leave the buffer empty; asserting order pins the invariant.
    expect(inst._log).toEqual(['reset', 'write:snap'])
  })

  it('is_snapshot=false: writes without resetting (incremental catch-up)', () => {
    const inst = makeStubInstance()
    applyTerminalData(inst, new TextEncoder().encode('delta'), false, 5, 0)

    expect(inst._log).toEqual(['write:delta'])
    expect(inst.terminal.reset).not.toHaveBeenCalled()
  })

  it('snapshot after incremental: wipes the previous state before writing the new payload', () => {
    // When an incremental event is followed by a snapshot (backend
    // decided the client had fallen behind), the snapshot's reset()
    // must fire before the write — otherwise the xterm ends up holding
    // "abc" + "xyz" instead of just "xyz".
    const inst = makeStubInstance()
    applyTerminalData(inst, new TextEncoder().encode('abc'), false, 3, 0)
    applyTerminalData(inst, new TextEncoder().encode('xyz'), true, 3, 3)

    expect(inst._log).toEqual(['write:abc', 'reset', 'write:xyz'])
  })

  it('returns endOffset on incremental writes when greater than current', () => {
    const inst = makeStubInstance()
    expect(applyTerminalData(inst, new TextEncoder().encode('a'), false, 1, 0)).toBe(1)
    expect(applyTerminalData(inst, new TextEncoder().encode('bc'), false, 3, 1)).toBe(3)
  })

  it('returns endOffset on snapshot writes', () => {
    const inst = makeStubInstance()
    expect(applyTerminalData(inst, new TextEncoder().encode('reset'), true, 100, 42)).toBe(100)
  })

  it('does not decrease the cursor on an incremental event with a smaller end_offset', () => {
    // Defensive: single-channel delivery should be ordered, but the
    // max-guard means out-of-order or duplicate increments can't rewind
    // the resume cursor and trigger spurious snapshot replays on the
    // next resubscribe.
    const inst = makeStubInstance()
    expect(applyTerminalData(inst, new TextEncoder().encode('late'), false, 50, 100)).toBe(100)
  })

  it('snapshot returns endOffset even when smaller than current', () => {
    // A snapshot resets xterm to match exactly `endOffset`; keeping a
    // larger stale cursor would tell the backend on resubscribe that we
    // have bytes we don't, silently skipping bytes on the next catch-up.
    const inst = makeStubInstance()
    expect(applyTerminalData(inst, new TextEncoder().encode('snap'), true, 50, 100)).toBe(50)
  })

  it('sets and clears suppressInput around snapshot writes', () => {
    // suppressInput silences xterm's automatic replies to DA/DSR/DECRQSS
    // queries inside the replayed bytes. It must be true while writing
    // the snapshot and released in the write callback so live user input
    // afterwards is delivered normally.
    const inst = makeStubInstance()
    let suppressedDuringWrite = false
    ;(inst.terminal.write as any).mockImplementation((_data: Uint8Array, cb?: () => void) => {
      suppressedDuringWrite = inst.suppressInput
      cb?.()
    })

    applyTerminalData(inst, new TextEncoder().encode('snap'), true, 4, 0)

    expect(suppressedDuringWrite).toBe(true)
    expect(inst.suppressInput).toBe(false)
  })

  it('does not touch suppressInput on incremental writes', () => {
    // Incremental data is live PTY output; xterm's replies to escape
    // sequences in it ARE legitimate user-visible behavior and must
    // reach the PTY (e.g. cursor position reports).
    const inst = makeStubInstance()
    applyTerminalData(inst, new TextEncoder().encode('delta'), false, 5, 0)

    expect(inst.suppressInput).toBe(false)
  })

  it('invokes onParsed after snapshot write completes', () => {
    const inst = makeStubInstance()
    const onParsed = vi.fn()

    applyTerminalData(inst, new TextEncoder().encode('snap'), true, 4, 0, onParsed)
    expect(onParsed).toHaveBeenCalledTimes(1)
  })

  it('invokes onParsed on incremental writes', () => {
    const inst = makeStubInstance()
    const onParsed = vi.fn()

    applyTerminalData(inst, new TextEncoder().encode('delta'), false, 5, 0, onParsed)
    expect(onParsed).toHaveBeenCalledTimes(1)
  })
})
