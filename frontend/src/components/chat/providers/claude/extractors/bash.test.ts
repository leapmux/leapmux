import { describe, expect, it } from 'vitest'
import { commandStatusLabel } from '../../../results/commandResult'
import { claudeBashFromToolResult } from './bash'

describe('claudeBashFromToolResult', () => {
  it('falls back to raw content when toolUseResult is missing', () => {
    expect(claudeBashFromToolResult({
      resultContent: 'plain output',
      isError: false,
    })).toEqual({
      output: 'plain output',
      isError: false,
    })
  })

  it('extracts stdout/stderr/interrupted', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: {
        stdout: 'STDOUT',
        stderr: 'STDERR',
        interrupted: true,
      },
      resultContent: 'unused',
      isError: false,
    })
    expect(source.output).toBe('STDOUT\nSTDERR')
    expect(source.stderr).toBe('STDERR')
    expect(source.interrupted).toBe(true)
    expect(source.isError).toBe(true)
  })

  it('uses stdout when stderr is empty', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: { stdout: 'just stdout' },
      resultContent: 'unused',
      isError: false,
    })
    expect(source.output).toBe('just stdout')
    expect(source.stderr).toBeUndefined()
  })

  it('marks isError when caller passes isError=true', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: { stdout: 'x' },
      resultContent: '',
      isError: true,
    })
    expect(source.isError).toBe(true)
  })
})

describe('commandStatusLabel', () => {
  it('returns "Interrupted" when interrupted', () => {
    expect(commandStatusLabel({ output: '', isError: true, interrupted: true })).toBe('Interrupted')
  })

  it('returns "Error (exit N)" when exitCode is non-zero', () => {
    expect(commandStatusLabel({ output: '', isError: true, exitCode: 5 })).toBe('Error (exit 5)')
  })

  it('returns "Error" when isError without exit code', () => {
    expect(commandStatusLabel({ output: '', isError: true })).toBe('Error')
  })

  it('returns "Success" otherwise', () => {
    expect(commandStatusLabel({ output: 'ok', isError: false })).toBe('Success')
    expect(commandStatusLabel({ output: 'ok', isError: false, exitCode: 0 })).toBe('Success')
  })
})

// A failed Bash arrives as a TEXT tool_result whose first line is the exit code:
//   "Exit code 1\nmv: rename absent.txt ...: No such file or directory"
// The structured payload (stdout/stderr/interrupted) carries no exit code, so the
// marker is the only place it exists. Without it the row reads "Error" where
// OpenCode, Pi and ZCode all read "Error (exit 1)" for the same failed command.
describe('claudeBashFromToolResult exit-code marker', () => {
  const failure = 'Exit code 1\nmv: rename absent-rn.txt to renamed-rn.txt: No such file or directory'

  it('reads the exit code from the marker the text result begins with', () => {
    const source = claudeBashFromToolResult({ resultContent: failure, isError: true })
    expect(source.exitCode).toBe(1)
    expect(commandStatusLabel(source)).toBe('Error (exit 1)')
  })

  it('drops the consumed marker from the body, so the code is shown once', () => {
    const source = claudeBashFromToolResult({ resultContent: failure, isError: true })
    expect(source.output).toBe('mv: rename absent-rn.txt to renamed-rn.txt: No such file or directory')
  })

  it('reads a multi-digit code', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 127\nnot found', isError: true })
    expect(source.exitCode).toBe(127)
    expect(source.output).toBe('not found')
  })

  // A marker with nothing after it is the whole result. The body then has nothing
  // to show, and inventing one would be worse than an empty body.
  it('accepts a marker that is the entire result', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 2', isError: true })
    expect(source.exitCode).toBe(2)
    expect(source.output).toBe('')
  })

  // Exit code zero is a real value and not an absent one. `commandStatusLabel`
  // already refuses to call it an error, so carrying it changes no label.
  it('carries a zero exit code without calling it a failure', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 0\ndone', isError: false })
    expect(source.exitCode).toBe(0)
    expect(commandStatusLabel(source)).toBe('Success')
  })

  // Output that merely MENTIONS the words must not be read as the marker, and the
  // marker only counts at the very start of the result.
  it('ignores the words anywhere but the first line', () => {
    const source = claudeBashFromToolResult({ resultContent: 'checking\nExit code 1\n', isError: true })
    expect(source.exitCode).toBeUndefined()
    expect(source.output).toBe('checking\nExit code 1\n')
  })

  it('ignores a first line that only resembles the marker', () => {
    for (const text of ['Exit code\nx', 'Exit code abc\nx', 'Exit codes 1\nx', 'exit code 1\nx']) {
      const source = claudeBashFromToolResult({ resultContent: text, isError: true })
      expect(source.exitCode).toBeUndefined()
      expect(source.output).toBe(text)
    }
  })

  // The structured payload keeps its own shape: its stdout/stderr carry the command's
  // own bytes, and a marker inside them is the command's output rather than Claude's.
  it('leaves the structured payload alone', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: { stdout: 'Exit code 1\nreal output' },
      resultContent: 'unused',
      isError: true,
    })
    expect(source.exitCode).toBeUndefined()
    expect(source.output).toBe('Exit code 1\nreal output')
  })
})
