import type { MiMoToolPart } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { mimoExecOutcome, mimoExecuteRequest, mimoExecuteResult } from './execute'

/** One finished execution call. */
function executePart(tool: string, fields: Partial<MiMoToolPart>): MiMoToolPart {
  return { callId: 'call-1', tool, status: 'completed', input: {}, output: '', error: '', title: '', metadata: {}, attachments: [], ...fields }
}

describe('mimoExecuteRequest', () => {
  it('reads a shell command, its description and its directory', () => {
    expect(mimoExecuteRequest(MIMO_TOOL.Bash, { command: 'ls', description: 'List', workdir: '/p' })).toEqual({ command: 'ls', description: 'List', cwd: '/p' })
    expect(mimoExecuteRequest(MIMO_TOOL.Bash, { command: 'ls' })).toEqual({ command: 'ls' })
  })

  it('reads a script as JavaScript, with no directory', () => {
    expect(mimoExecuteRequest(MIMO_TOOL.Exec, { code: 'return 2', description: 'Sum', workdir: '/p' })).toEqual({ command: 'return 2', language: 'javascript', description: 'Sum' })
  })

  it('reads an empty command from arguments that state none', () => {
    expect(mimoExecuteRequest(MIMO_TOOL.Bash, {})).toEqual({ command: '' })
    expect(mimoExecuteRequest(MIMO_TOOL.Exec, {})).toEqual({ command: '', language: 'javascript' })
  })

  // `bash` spells its command `command`, and `exec` spells it `code`. Neither reads
  // the other's key.
  it('reads each tool\'s own key for the command', () => {
    expect(mimoExecuteRequest(MIMO_TOOL.Bash, { code: 'return 2' })).toEqual({ command: '' })
    expect(mimoExecuteRequest(MIMO_TOOL.Exec, { command: 'ls' })).toEqual({ command: '', language: 'javascript' })
  })
})

describe('mimoExecuteResult', () => {
  it('reads the output and the exit code of a shell command from the metadata', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { output: 'hi\n\n<bash_metadata>note</bash_metadata>', metadata: { output: 'hi\n', exit: 0 } })))
      .toEqual({ commands: [{ output: 'hi\n', exitCode: 0 }], unresolvedTerminals: [] })
  })

  it('states a truncated output', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', exit: 1, truncated: true } })))
      .toEqual({ commands: [{ output: 'a', exitCode: 1, truncated: true }], unresolvedTerminals: [] })
  })

  it('states no exit code that is not a whole number', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', exit: 1.5 } })).commands[0]).toEqual({ output: 'a' })
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', exit: '0' } })).commands[0]).toEqual({ output: 'a' })
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', exit: Number.NaN } })).commands[0]).toEqual({ output: 'a' })
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', exit: 2 ** 53 } })).commands[0]).toEqual({ output: 'a' })
  })

  // A shell reports a command that a signal killed with a negative code on some
  // platforms. It is still a whole number, and the card states it.
  it('keeps a negative exit code', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: '', exit: -1 } })).commands[0]).toEqual({ output: '', exitCode: -1 })
  })

  // The metadata states what the command printed, and the `output` field adds a note
  // for the model. A command that printed nothing states an empty output, not the note.
  it('reads an empty metadata output as the output, not the note for the model', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { output: '<bash_metadata>no output</bash_metadata>', metadata: { output: '', exit: 0 } })).commands[0])
      .toEqual({ output: '', exitCode: 0 })
  })

  it('reads the output field of a shell command whose metadata states no output', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { output: 'hi\n', metadata: {} })).commands[0]).toEqual({ output: 'hi\n' })
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { output: 'hi\n', metadata: { output: 7 } })).commands[0]).toEqual({ output: 'hi\n' })
  })

  it('states no truncation that is not true', () => {
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', truncated: 'yes' } })).commands[0]).toEqual({ output: 'a' })
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Bash, { metadata: { output: 'a', truncated: false } })).commands[0]).toEqual({ output: 'a' })
  })

  // A script states its answer in `output` alone: its metadata holds the calls it
  // made, and no `output` or `exit` field.
  it('reads the answer of a script from its output', () => {
    const output = '<exec status="completed">\n<return_value>\n2\n</return_value>\n</exec>'
    expect(mimoExecuteResult(executePart(MIMO_TOOL.Exec, { output, metadata: { status: 'completed', toolCalls: 1 } })))
      .toEqual({ commands: [{ output }], unresolvedTerminals: [] })
  })
})

describe('mimoExecOutcome', () => {
  it.each([
    ['completed', null],
    ['code_error', 'failed'],
    ['timeout', 'failed'],
    ['budget_exceeded', 'failed'],
    ['cancelled', 'cancelled'],
  ])('reads a script that ended with %s', (status, outcome) => {
    expect(mimoExecOutcome(executePart(MIMO_TOOL.Exec, { metadata: { status } }))).toBe(outcome)
  })

  it('reads no outcome from a script that states no status', () => {
    expect(mimoExecOutcome(executePart(MIMO_TOOL.Exec, { metadata: {} }))).toBeNull()
  })

  // A status word from a later release is an ending this build cannot read, so it
  // claims no failure that MiMo did not state.
  it('reads no outcome from a status of a later release', () => {
    expect(mimoExecOutcome(executePart(MIMO_TOOL.Exec, { metadata: { status: 'paused' } }))).toBeNull()
  })

  it('reads no outcome from a shell command', () => {
    expect(mimoExecOutcome(executePart(MIMO_TOOL.Bash, { metadata: { status: 'code_error' } }))).toBeNull()
  })
})
