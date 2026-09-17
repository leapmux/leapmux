import { describe, expect, it } from 'vitest'
import { commandExit, commandStatusLabel } from '../../../ir/commandResult'
import { claudeBashFromToolResult, claudeExecutePayload } from './execute'
import { claudeRequestFor } from './toolRequests'

// The outcome words live on the row's status, not on the source: `interrupted`
// and `isError` became `status: 'cancelled' | 'failed'` at the row level, and
// `commandStatusLabel` reads that status beside the exit code.

describe('claudeBashFromToolResult', () => {
  it('falls back to raw content when toolUseResult is missing', () => {
    expect(claudeBashFromToolResult({
      resultContent: 'plain output',
    })).toEqual({
      output: 'plain output',
    })
  })

  it('joins stdout and stderr into the output; an interrupted payload words the row cancelled, not the source', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: {
        stdout: 'STDOUT',
        stderr: 'STDERR',
        interrupted: true,
      },
      resultContent: 'unused',
    })
    // Both streams reach the reader through `output`; the row carries no separate
    // stderr field, because nothing drew one.
    expect(source.output).toBe('STDOUT\nSTDERR')
    expect(commandStatusLabel('cancelled', commandExit(source))).toBe('Interrupted')
  })

  it('uses stdout when stderr is empty', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: { stdout: 'just stdout' },
      resultContent: 'unused',
    })
    expect(source.output).toBe('just stdout')
  })

  it('words a failed row Error even when the payload carries no exit code', () => {
    const source = claudeBashFromToolResult({
      toolUseResult: { stdout: 'x' },
      resultContent: '',
    })
    expect(commandStatusLabel('failed', commandExit(source))).toBe('Error')
  })
})

describe('commandStatusLabel', () => {
  it('returns "Declined" for a refused call', () => {
    expect(commandStatusLabel('declined', {})).toBe('Declined')
  })

  it('returns "Interrupted" when the row was cancelled', () => {
    expect(commandStatusLabel('cancelled', {})).toBe('Interrupted')
  })

  it('returns "Error (exit N)" when exitCode is non-zero', () => {
    expect(commandStatusLabel('completed', { exitCode: 5 })).toBe('Error (exit 5)')
  })

  it('returns "Error" for a failed status word beside a zero exit code', () => {
    expect(commandStatusLabel('failed', { exitCode: 0 })).toBe('Error')
  })

  it('returns "Error" when failed without an exit code', () => {
    expect(commandStatusLabel('failed', {})).toBe('Error')
  })

  it('returns "Success" otherwise', () => {
    expect(commandStatusLabel('completed', {})).toBe('Success')
  })
})

// A failed Bash arrives as a TEXT tool_result whose first line is the exit code:
//   "Exit code 1\nmv: rename absent.txt ...: No such file or directory"
// The structured payload (stdout/stderr) carries no exit code, so the
// marker is the only place it exists. Without it the row reads "Error" where
// OpenCode, Pi and ZCode all read "Error (exit 1)" for the same failed command.
describe('claudeBashFromToolResult exit-code marker', () => {
  const failure = 'Exit code 1\nmv: rename absent-rn.txt to renamed-rn.txt: No such file or directory'

  it('reads the exit code from the marker the text result begins with', () => {
    const source = claudeBashFromToolResult({ resultContent: failure })
    expect(source.exitCode).toBe(1)
    expect(commandStatusLabel('failed', commandExit(source))).toBe('Error (exit 1)')
  })

  it('drops the consumed marker from the body, so the code is shown once', () => {
    const source = claudeBashFromToolResult({ resultContent: failure })
    expect(source.output).toBe('mv: rename absent-rn.txt to renamed-rn.txt: No such file or directory')
  })

  it('reads a multi-digit code', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 127\nnot found' })
    expect(source.exitCode).toBe(127)
    expect(source.output).toBe('not found')
  })

  // A marker with nothing after it is the whole result. The body then has nothing
  // to show, and inventing one would be worse than an empty body.
  it('accepts a marker that is the entire result', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 2' })
    expect(source.exitCode).toBe(2)
    expect(source.output).toBe('')
  })

  // Exit code zero is a real value and not an absent one. `commandStatusLabel`
  // already refuses to call it an error, so carrying it changes no label.
  it('carries a zero exit code without calling it a failure', () => {
    const source = claudeBashFromToolResult({ resultContent: 'Exit code 0\ndone' })
    expect(source.exitCode).toBe(0)
    expect(commandStatusLabel('completed', commandExit(source))).toBe('Success')
  })

  // Output that merely MENTIONS the words must not be read as the marker, and the
  // marker only counts at the very start of the result.
  it('ignores the words anywhere but the first line', () => {
    const source = claudeBashFromToolResult({ resultContent: 'checking\nExit code 1\n' })
    expect(source.exitCode).toBeUndefined()
    expect(source.output).toBe('checking\nExit code 1\n')
  })

  it('ignores a first line that only resembles the marker', () => {
    for (const text of ['Exit code\nx', 'Exit code abc\nx', 'Exit codes 1\nx', 'exit code 1\nx']) {
      const source = claudeBashFromToolResult({ resultContent: text })
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
    })
    expect(source.exitCode).toBeUndefined()
    expect(source.output).toBe('Exit code 1\nreal output')
  })
})

/**
 * Claude Code sends a one-line `description` beside every `Bash` command, and the row
 * header is the only place a reader sees it. Without it the header states the generic
 * `Run command`, which says nothing the terminal icon does not.
 */
describe('claudeExecutePayload description', () => {
  const request = (input: Record<string, unknown>) =>
    claudeRequestFor('execute', input, { toolName: 'Bash', result: undefined, context: {} })

  it('carries the description the agent sent', () => {
    const payload = claudeExecutePayload(request({ command: 'ls -la', description: 'List files in current directory' }), undefined)
    expect(payload.request.description).toBe('List files in current directory')
  })

  it('states no description for a command that carries none', () => {
    expect(claudeExecutePayload(request({ command: 'ls -la' }), undefined).request.description).toBeUndefined()
  })
})
