import { describe, expect, it } from 'vitest'
import { ohMyPiCommandOutcome, ohMyPiEvalRequest, ohMyPiEvalResults } from './execute'

describe('ohMyPiCommandOutcome', () => {
  it('keeps the output and drops the wall time of a command that succeeded', () => {
    // omp 18.2.11's own result text (probe s1).
    const outcome = ohMyPiCommandOutcome('probe-output\n\n\nWall time: 0.05 seconds', { timeoutSeconds: 300, wallTimeMs: 49.99 }, true)
    expect(outcome.result).toEqual({ output: 'probe-output', exitCode: 0, durationMs: 49.99 })
    expect(outcome.cancelled).toBe(false)
    expect(outcome.timedOut).toBe(false)
  })

  it('reads the exit code of a command that failed', () => {
    const outcome = ohMyPiCommandOutcome('out\n\n\nWall time: 0.07 seconds\n\nCommand exited with code 3', { wallTimeMs: 65, exitCode: 3 }, false)
    expect(outcome.result).toEqual({ output: 'out', exitCode: 3, durationMs: 65 })
  })

  it('reads the exit code from the text when the details state none', () => {
    expect(ohMyPiCommandOutcome('boom\n\nCommand exited with code 2', {}, false).result.exitCode).toBe(2)
    expect(ohMyPiCommandOutcome('boom\n\nCommand exited with code -1', {}, false).result.exitCode).toBe(-1)
  })

  it('states a timeout and no exit code', () => {
    const outcome = ohMyPiCommandOutcome('partial\n\n[Command timed out after 5 seconds]', { timedOut: true, timeoutSeconds: 5 }, false)
    expect(outcome.timedOut).toBe(true)
    expect(outcome.result.output).toBe('partial')
    expect(outcome.result.exitCode).toBeUndefined()
  })

  it('states a stop the reader asked for, in either spelling', () => {
    const aborted = ohMyPiCommandOutcome('some output\n\n[Command aborted]', {}, false)
    expect(aborted.cancelled).toBe(true)
    expect(aborted.result.output).toBe('some output')
    const cancelled = ohMyPiCommandOutcome('[Command cancelled]\nsome output', {}, false)
    expect(cancelled.cancelled).toBe(true)
    expect(cancelled.result.output).toBe('some output')
    expect(ohMyPiCommandOutcome('Command aborted', {}, false)).toMatchObject({ cancelled: true, result: { output: '' } })
  })

  it('states no exit code for a failure that reported none', () => {
    expect(ohMyPiCommandOutcome('Command failed: missing exit status', {}, false).result.exitCode).toBeUndefined()
  })

  it('states no exit code for the partial output of a call that did not end', () => {
    expect(ohMyPiCommandOutcome('so far', {}, false).result).toEqual({ output: 'so far' })
  })

  it('keeps output lines that only look like a notice when they are not the last', () => {
    const outcome = ohMyPiCommandOutcome('Wall time: 3 seconds\nreal output', { wallTimeMs: 1 }, true)
    expect(outcome.result.output).toBe('Wall time: 3 seconds\nreal output')
  })

  it('marks an output omp truncated', () => {
    expect(ohMyPiCommandOutcome('tail', { meta: { truncation: { totalBytes: 90000 } } }, true).result.truncated).toBe(true)
  })

  it('reads an empty output', () => {
    expect(ohMyPiCommandOutcome('', {}, true).result).toEqual({ output: '', exitCode: 0 })
  })

  it('reads a text with Windows line endings', () => {
    expect(ohMyPiCommandOutcome('a\r\nb\r\n\r\nWall time: 0.1 seconds\r\n\r\nCommand exited with code 1', {}, false).result).toEqual({ output: 'a\nb', exitCode: 1 })
  })

  it('prefers the exit code the details state to the one the text states', () => {
    expect(ohMyPiCommandOutcome('x\n\nCommand exited with code 4', { exitCode: 3 }, false).result.exitCode).toBe(3)
  })

  it('reads a stop that omp states on the same line as the output', () => {
    expect(ohMyPiCommandOutcome('[Command cancelled] partial\nmore', {}, false)).toMatchObject({ cancelled: true, result: { output: 'partial\nmore' } })
  })

  it('states no exit code of zero for a command that ended by a timeout or a stop', () => {
    // The notice or the flag states that the command did not exit by itself, so an
    // end frame with no error flag does not make its exit code zero.
    const timedOut = ohMyPiCommandOutcome('partial\n\nCommand timed out', {}, true)
    expect(timedOut).toMatchObject({ timedOut: true, cancelled: false, result: { output: 'partial' } })
    expect(timedOut.result.exitCode).toBeUndefined()
    const flagged = ohMyPiCommandOutcome('partial', { timedOut: true }, true)
    expect(flagged.timedOut).toBe(true)
    expect(flagged.result.exitCode).toBeUndefined()
    const stopped = ohMyPiCommandOutcome('partial\n\n[Command aborted]', {}, true)
    expect(stopped.cancelled).toBe(true)
    expect(stopped.result.exitCode).toBeUndefined()
  })

  it('peels every notice omp appends, in any order, down to the output', () => {
    const outcome = ohMyPiCommandOutcome('out\n\n[Command timed out after 5 seconds]\n\nWall time: 5.00 seconds\n\nCommand exited with code 124\n\n', { wallTimeMs: 5000 }, false)
    expect(outcome).toEqual({ result: { output: 'out', exitCode: 124, durationMs: 5000 }, cancelled: false, timedOut: true })
  })

  it('states no truncation for a meta record that states none', () => {
    expect(ohMyPiCommandOutcome('x', { meta: {} }, true).result.truncated).toBeUndefined()
    expect(ohMyPiCommandOutcome('x', { meta: { truncation: 'yes' } }, true).result.truncated).toBeUndefined()
  })
})

describe('ohMyPiEvalResults', () => {
  it('reads one command result per cell, and counts omp\'s 0-based index from one', () => {
    // omp 18.2.11's `EvalCellResult`: the index starts at 0, and the status is one of
    // `pending`, `running`, `complete` and `error`.
    expect(ohMyPiEvalResults({
      cells: [
        { index: 0, title: 'Setup', code: 'x = 1', output: '', status: 'complete', durationMs: 4, exitCode: 0 },
        { index: 1, code: 'x + 1', output: '2', status: 'complete', exitCode: 0 },
        { index: 2, code: 'boom()', output: 'NameError', status: 'error', exitCode: 1 },
        'not a cell',
      ],
    })).toEqual([
      { output: '', label: 'Setup', durationMs: 4, exitCode: 0 },
      { output: '2', label: 'Cell 2', exitCode: 0 },
      { output: 'NameError', label: 'Cell 3', exitCode: 1 },
    ])
  })

  it('labels a cell that states no index from its place in the list', () => {
    expect(ohMyPiEvalResults({ cells: [{ output: 'a' }, { output: 'b' }] })?.map(cell => cell.label)).toEqual(['Cell 1', 'Cell 2'])
  })

  it('answers null for a result that states no cells', () => {
    expect(ohMyPiEvalResults({})).toBeNull()
    expect(ohMyPiEvalResults({ cells: [] })).toBeNull()
    expect(ohMyPiEvalResults({ cells: ['x'] })).toBeNull()
  })
})

describe('ohMyPiEvalRequest', () => {
  it('reads the code, the highlighter and the title', () => {
    expect(ohMyPiEvalRequest({ language: 'js', code: '1 + 1', title: 'Sum' })).toEqual({ command: '1 + 1', language: 'javascript', description: 'Sum' })
  })

  it('draws a Python cell as plain text', () => {
    expect(ohMyPiEvalRequest({ language: 'py', code: 'print(1)' })).toEqual({ command: 'print(1)' })
  })

  it('draws a cell of a language it does not know, or of no language, as plain text', () => {
    expect(ohMyPiEvalRequest({ language: 'ruby', code: 'puts 1' })).toEqual({ command: 'puts 1' })
    expect(ohMyPiEvalRequest({})).toEqual({ command: '' })
  })
})
