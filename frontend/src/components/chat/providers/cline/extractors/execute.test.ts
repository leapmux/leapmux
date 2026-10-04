import { describe, expect, it } from 'vitest'
import { clineCommandRequest, clineCommandResults } from './execute'

describe('clineCommandRequest', () => {
  it('states each command on a line, a program with its arguments included', () => {
    expect(clineCommandRequest({ commands: ['git status', { command: 'go', args: ['test', './...'] }] })).toEqual({ command: 'git status\ngo test ./...', language: 'bash' })
  })

  it('reads the one command of a call that states `command`', () => {
    expect(clineCommandRequest({ command: 'make test' })).toEqual({ command: 'make test', language: 'bash' })
  })

  // An entry that states no command adds no empty line, and an argument that is not
  // text adds no word.
  it('leaves out an entry that states no command, and an argument that is not text', () => {
    expect(clineCommandRequest({ commands: ['', { args: [] }, 3, null, { command: 'ls', args: ['-a', 3, null] }] })).toEqual({ command: 'ls -a', language: 'bash' })
  })

  it('states an empty command for a call that states none', () => {
    for (const args of [{}, { commands: [] }, { commands: 'ls' }, { command: 3 }])
      expect(clineCommandRequest(args), JSON.stringify(args)).toEqual({ command: '', language: 'bash' })
  })
})

describe('clineCommandResults', () => {
  it('states one result for each command, labeled by its command', () => {
    expect(clineCommandResults([
      { query: 'echo a', result: 'a\n', success: true },
      { query: 'echo b', result: 'b\n', success: true },
    ])).toEqual([{ output: 'a\n', label: 'echo a' }, { output: 'b\n', label: 'echo b' }])
  })

  // The record of Cline 3.0.64 for `echo "cline-fail-$((70 + 7))" >&2; exit 3`, as the
  // model received it in an E2E run. The code opens the result and is the whole error.
  it('reads the exit code of a command that exited with one, and drops the words that state it', () => {
    const query = 'echo "cline-fail-$((70 + 7))" >&2; exit 3'
    expect(clineCommandResults([{ query, result: '[Command exited with code 3]\n\n[stderr]\ncline-fail-77\n', error: 'Command exited with code 3', success: false }]))
      .toEqual([{ output: '\n[stderr]\ncline-fail-77\n', exitCode: 3, label: query }])
  })

  it('reads the exit code of a command that printed nothing, or a note after its output', () => {
    expect(clineCommandResults([{ query: 'false', result: '[Command exited with code 1]', error: 'Command exited with code 1', success: false }]))
      .toEqual([{ output: '', exitCode: 1, label: 'false' }])
    expect(clineCommandResults([{ query: 'make', result: '[Command exited with code 2]\npartial\nThe process ended.', error: 'Command exited with code 2', success: false }]))
      .toEqual([{ output: 'partial\nThe process ended.', exitCode: 2, label: 'make' }])
  })

  // The error is enough to give the code. A result that does not open with Cline's line
  // is the command's output as it stands.
  it('reads the exit code from the error when the result does not open with it', () => {
    expect(clineCommandResults([{ query: 'make', result: 'partial', error: 'Command exited with code 2', success: false }]))
      .toEqual([{ output: 'partial', exitCode: 2, label: 'make' }])
    expect(clineCommandResults([{ query: 'make', result: 'out\n[Command exited with code 2]', error: 'Command exited with code 2', success: false }]))
      .toEqual([{ output: 'out\n[Command exited with code 2]', exitCode: 2, label: 'make' }])
  })

  it('keeps a line that states another code than the error', () => {
    expect(clineCommandResults([{ query: 'make', result: '[Command exited with code 4]\nout', error: 'Command exited with code 3', success: false }]))
      .toEqual([{ output: '[Command exited with code 4]\nout', exitCode: 3, label: 'make' }])
  })

  it('reads a negative exit code and an error with space around it', () => {
    expect(clineCommandResults([{ query: 'x', result: '[Command exited with code -1]\nout', error: ' Command exited with code -1\n', success: false }]))
      .toEqual([{ output: 'out', exitCode: -1, label: 'x' }])
  })

  // A command that succeeded has no exit code to read, whatever its output prints.
  it('reads no exit code from a command that succeeded', () => {
    expect(clineCommandResults([{ query: 'cat log', result: '[Command exited with code 1]\nlog', success: true }]))
      .toEqual([{ output: '[Command exited with code 1]\nlog', label: 'cat log' }])
  })

  // Cline states a code other than zero only. A zero, or a number that no platform
  // reports, is not a code to head the row with, so the error keeps its words.
  it('reads no exit code of zero or past the safe integers, and keeps the error', () => {
    expect(clineCommandResults([{ query: 'x', result: '[Command exited with code 0]\nout', error: 'Command exited with code 0', success: false }]))
      .toEqual([{ output: '[Command exited with code 0]\nout\nCommand exited with code 0', failed: true, label: 'x' }])
    expect(clineCommandResults([{ query: 'x', result: 'out', error: 'Command exited with code 99999999999999999999', success: false }]))
      .toEqual([{ output: 'out\nCommand exited with code 99999999999999999999', failed: true, label: 'x' }])
  })

  // A command that did not start, or that ran out of time, states no code: Cline's
  // error is the only statement of why.
  it('puts the error of a command that failed with no exit code on a line after its output', () => {
    expect(clineCommandResults([{ query: 'make', result: '', error: 'Command failed: spawn ENOENT', success: false }]))
      .toEqual([{ output: 'Command failed: spawn ENOENT', failed: true, label: 'make' }])
    expect(clineCommandResults([{ query: 'make', result: 'partial', error: 'Command failed: Command timed out after 30000ms', success: false }]))
      .toEqual([{ output: 'partial\nCommand failed: Command timed out after 30000ms', failed: true, label: 'make' }])
    expect(clineCommandResults([{ query: 'make', result: 'partial\n', error: 'Command failed: x', success: false }]))
      .toEqual([{ output: 'partial\nCommand failed: x', failed: true, label: 'make' }])
  })

  it('states no label for a result that states no command', () => {
    expect(clineCommandResults([{ query: '', result: 'x', success: true }])).toEqual([{ output: 'x' }])
  })

  it('reads a stored result, which Cline keeps as JSON text', () => {
    expect(clineCommandResults(JSON.stringify([{ query: 'ls', result: 'a.ts\n', success: true }]))).toEqual([{ output: 'a.ts\n', label: 'ls' }])
  })

  it('states no result for an output that holds no record', () => {
    for (const output of [undefined, null, 'plain text', { error: 'boom' }])
      expect(clineCommandResults(output), JSON.stringify(output)).toEqual([])
  })
})
