import type { CommandResult } from '../model/commandResult'
import { describe, expect, it } from 'vitest'
import { normalizedCommandBody, PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { commandExit, commandIsError } from '../model/commandResult'
import { commandCollapseThreshold, commandOutputIsCollapsible, commandStatusLabel, normalizedCommandOutput } from './commandResult'

describe('commandStatusLabel', () => {
  it('words a refusal Declined', () => {
    expect(commandStatusLabel('declined', {})).toBe('Declined')
  })

  it('words a cancellation Interrupted', () => {
    expect(commandStatusLabel('cancelled', {})).toBe('Interrupted')
    expect(commandStatusLabel('cancelled', { exitCode: 1 })).toBe('Interrupted')
  })

  it('words a known non-zero exit code Error (exit N), whatever the status says', () => {
    expect(commandStatusLabel('completed', { exitCode: 7 })).toBe('Error (exit 7)')
    expect(commandStatusLabel('failed', { exitCode: 7 })).toBe('Error (exit 7)')
  })

  // The status outranks a zero exit code, as it did before the row model: a row the
  // provider called failed must not word itself "Success", because the header that
  // states the outcome is suppressed whenever the label IS the success word.
  it('words a failed status word Error even beside a known zero exit code', () => {
    expect(commandStatusLabel('failed', { exitCode: 0 })).toBe('Error')
    expect(commandStatusLabel('completed', { exitCode: 0 })).toBe('Success')
  })

  it('words an unknown exit code from the status alone', () => {
    expect(commandStatusLabel('failed', {})).toBe('Error')
    expect(commandStatusLabel('failed', { exitCode: null })).toBe('Error')
    expect(commandStatusLabel('completed', {})).toBe('Success')
    expect(commandStatusLabel('in_progress', {})).toBe('Success')
  })

  // A host terminal the OS ended reports a signal and NO exit code. Before the row
  // read it the process was stored with its signal and the row stated neither a code
  // nor a reason.
  it('words a signal that ended the process when no exit code describes it', () => {
    expect(commandStatusLabel('completed', { signal: 'killed' })).toBe('Error (killed)')
    expect(commandStatusLabel('completed', { signal: 'segmentation fault' })).toBe('Error (segmentation fault)')
  })

  // The signal outranks a bare `failed`, because it says WHY. The reader's own stop
  // still outranks both, one branch above.
  it('words a failed call that a signal ended with the signal', () => {
    expect(commandStatusLabel('failed', { signal: 'killed' })).toBe('Error (killed)')
    expect(commandStatusLabel('failed', {})).toBe('Error')
  })

  it('keeps the reader own stop worded Interrupted, signal or not', () => {
    expect(commandStatusLabel('cancelled', { signal: 'killed' })).toBe('Interrupted')
    expect(commandStatusLabel('declined', { signal: 'killed' })).toBe('Declined')
  })
})

describe('commandExit', () => {
  // A code and a signal cannot BOTH be spelled -- `CommandExit` is a union, so
  // `{ exitCode: 0, signal: 'killed' }` is a compile error. This states which half a
  // source answers with, which is what the label and the glyph read.
  // A cast can still build the pair the type forbids. `commandExit` and
  // `commandStatusLabel` must then word the row the same way, which they only do
  // because both read the CODE first.
  it('reads the code first, as the label does', () => {
    const both = { output: '', exitCode: 1, signal: 'killed' } as unknown as CommandResult
    expect(commandExit(both)).toEqual({ exitCode: 1 })
    expect(commandStatusLabel('completed', commandExit(both))).toBe('Error (exit 1)')
  })

  it('answers the half the source carries', () => {
    expect(commandExit({ output: '', exitCode: 7 })).toEqual({ exitCode: 7 })
    expect(commandExit({ output: '', signal: 'killed' })).toEqual({ signal: 'killed' })
    expect(commandExit({ output: '' })).toEqual({ exitCode: undefined })
  })
})

describe('commandIsError', () => {
  it('reads a known exit code, and a signal only where none is known', () => {
    expect(commandIsError({ exitCode: 0 })).toBe(false)
    expect(commandIsError({ exitCode: 1 })).toBe(true)
    expect(commandIsError({ exitCode: null })).toBe(false)
    expect(commandIsError({})).toBe(false)
    expect(commandIsError({ signal: 'killed' })).toBe(true)
  })
})

// The toolbar's collapse check and the BODY must reach the same answer, or the row
// offers an Expand over output that clips nothing -- or hides one over output it does
// clip. Both read the same memoized normalize, which is the point: the check used to
// normalize only, while the body normalizes AND strips the leading blank lines.
describe('commandOutputIsCollapsible', () => {
  it('agrees with the body about output that leads with blank lines', () => {
    // Three blank lines the body strips, over three content lines it does not clip.
    const leadingBlanks = '\n\n\none\ntwo\nthree'
    expect(normalizedCommandBody(leadingBlanks).text).toBe('one\ntwo\nthree')
    expect(commandOutputIsCollapsible({ output: leadingBlanks })).toBe(false)
  })

  it('offers the expand for output that really is longer than the collapsed rows', () => {
    expect(commandOutputIsCollapsible({ output: 'one\ntwo\nthree\nfour' })).toBe(true)
  })

  it.each([
    ['', false],
    ['one', false],
    ['one\ntwo\nthree', false],
  ])('answers %j with %s', (text, expected) => {
    expect(commandOutputIsCollapsible({ output: text })).toBe(expected)
  })

  // A `\r`-heavy run normalizes into separate lines, so the raw newline count
  // under-counts it. The widened threshold is what keeps the head/ellipsis/tail the
  // normalize step just produced from being sliced back off.
  it('counts the lines a carriage-return run normalizes into', () => {
    const progress = Array.from({ length: PROGRESS_MAX_ROWS + 4 }, (_, i) => `step ${i}`).join('\r')
    expect(progress.includes('\n')).toBe(false)
    expect(commandOutputIsCollapsible({ output: progress })).toBe(false)
    expect(commandCollapseThreshold(true)).toBe(PROGRESS_MAX_ROWS)
  })
})

/**
 * The normalize runs ONCE per command result object: the body component and the
 * collapsibility check read the same result of one revision, and a `\r`-heavy build
 * log paid for the whole regex/split/join twice per reactive pass before.
 */
describe('normalizedCommandOutput', () => {
  it('answers one result object with the same normalized value every time', () => {
    const command: CommandResult = { output: 'step 1\rstep 2\n' }
    const first = normalizedCommandOutput(command)
    expect(normalizedCommandOutput(command)).toBe(first)
    expect(first).toEqual({ text: 'step 1\nstep 2\n', hadCarriageReturns: true })
  })

  it('normalizes each result object separately', () => {
    const first: CommandResult = { output: 'a\nb\n' }
    const second: CommandResult = { output: 'a\nb\n' }
    expect(normalizedCommandOutput(second)).not.toBe(normalizedCommandOutput(first))
    expect(normalizedCommandOutput(first).text).toBe(normalizedCommandOutput(second).text)
  })

  // The one bound the WeakMap keeps: a normalized copy past 4 MiB is not retained,
  // because a running command produces a new one per frame and the row itself
  // already carries the raw text. The value still answers -- the body needs it --
  // but a second read builds a second copy, which is what a reference change states.
  it('still answers, but retains nothing, for a normalized copy past the cache cap', () => {
    const huge: CommandResult = { output: `${'x'.repeat(4 * 1024 * 1024 + 1)}\n` }
    const first = normalizedCommandOutput(huge)
    const second = normalizedCommandOutput(huge)
    expect(first).not.toBe(second)
    expect(first.text).toBe(second.text)
    expect(first.text.length).toBeGreaterThan(4 * 1024 * 1024)
  })
})
