import { describe, expect, it } from 'vitest'
import { ampCommandResult, ampShellCommand, ampShellOutput, ampShellTaskResult } from './execute'

describe('ampShellCommand', () => {
  it('reads `command` first, then the `cmd` of the legacy Bash tool', () => {
    expect(ampShellCommand({ command: 'ls', cmd: 'pwd' })).toBe('ls')
    expect(ampShellCommand({ cmd: 'pwd' })).toBe('pwd')
    expect(ampShellCommand({ command: '', cmd: 'pwd' })).toBe('pwd')
  })

  it('answers empty for a call that states no command as text', () => {
    expect(ampShellCommand({})).toBe('')
    expect(ampShellCommand({ command: 3, cmd: ['ls'] })).toBe('')
  })
})

describe('ampShellOutput', () => {
  it('reads a command that ended', () => {
    expect(ampShellOutput('{"output":"README.md\\n","exitCode":0}')).toEqual({ output: 'README.md\n', exitCode: 0, running: false, truncated: false, structured: true })
  })

  it('reads a command that Amp moved to the background', () => {
    expect(ampShellOutput('{"output":"started","running":true,"pid":4242}')).toEqual({ output: 'started', running: true, pid: 4242, truncated: false, structured: true })
  })

  it('reads the lines Amp dropped from the start of a long output', () => {
    expect(ampShellOutput('{"output":"tail","exitCode":1,"truncation":{"prefixLinesOmitted":120}}').truncated).toBe(true)
    expect(ampShellOutput('{"output":"all","exitCode":0,"truncation":{"prefixLinesOmitted":0}}').truncated).toBe(false)
  })

  it('reads text that is not Amp\'s record as the output itself', () => {
    for (const text of ['plain text', '', '[1,2]', '{"exitCode":0}', '{"output":3}'])
      expect(ampShellOutput(text), text).toEqual({ output: text, running: false, truncated: false, structured: false })
  })

  // A field of the wrong type states nothing: a string "0" is not an exit code, and a
  // string "true" does not make the command run.
  it('ignores a field whose type is wrong', () => {
    expect(ampShellOutput('{"output":"x","exitCode":"0","running":"true","pid":"42"}')).toEqual({ output: 'x', running: false, truncated: false, structured: true })
    expect(ampShellOutput('{"output":"x","truncation":{"prefixLinesOmitted":-3}}').truncated).toBe(false)
    expect(ampShellOutput('{"output":"x","truncation":"all"}').truncated).toBe(false)
  })
})

describe('ampCommandResult', () => {
  it('states the exit code of a command that ended', () => {
    expect(ampCommandResult('{"output":"x","exitCode":2}')).toEqual({ output: 'x', exitCode: 2 })
    expect(ampCommandResult('{"output":"x","exitCode":-1}')).toEqual({ output: 'x', exitCode: -1 })
  })

  it('states no exit code for a command that still runs, or for text Amp did not structure', () => {
    expect(ampCommandResult('{"output":"x","running":true,"pid":1}')).toEqual({ output: 'x' })
    expect(ampCommandResult('Error: shell failed')).toEqual({ output: 'Error: shell failed' })
  })

  // The command still runs, so a code in the record does not state how it ended.
  it('states no exit code for a command that runs, although the record states one', () => {
    expect(ampCommandResult('{"output":"x","exitCode":0,"running":true,"pid":1}')).toEqual({ output: 'x' })
  })

  it('marks an output whose start Amp dropped', () => {
    expect(ampCommandResult('{"output":"tail","exitCode":0,"truncation":{"prefixLinesOmitted":5}}')).toEqual({ output: 'tail', exitCode: 0, truncated: true })
  })
})

describe('ampShellTaskResult', () => {
  it('reads a status of a command that still runs, that ended, and that failed', () => {
    expect(ampShellTaskResult('{"output":"more","running":true,"pid":1}', false)).toEqual({ outcome: 'running', output: 'more' })
    expect(ampShellTaskResult('{"output":"done","exitCode":0,"running":false,"pid":1}', false)).toEqual({ outcome: 'completed', output: 'done' })
    expect(ampShellTaskResult('{"output":"boom","exitCode":3,"running":false,"pid":1}', false)).toEqual({ outcome: 'failed', output: 'boom' })
  })

  it('reads a kill as a stopped command', () => {
    expect(ampShellTaskResult('{"output":"","exitCode":143,"running":false,"pid":1}', true)).toEqual({ outcome: 'stopped', output: '' })
    expect(ampShellTaskResult('Stopped.', true)).toEqual({ outcome: 'stopped', output: 'Stopped.' })
  })

  it('reads text Amp did not structure as the output of a status', () => {
    expect(ampShellTaskResult('No tracked shell command for PID 9', false)).toEqual({ outcome: 'completed', output: 'No tracked shell command for PID 9' })
  })

  // The record states the state. A kill whose record says that the command still
  // runs did not stop it.
  it('reads a kill of a command that still runs as running', () => {
    expect(ampShellTaskResult('{"output":"x","running":true,"pid":1}', true)).toEqual({ outcome: 'running', output: 'x' })
  })

  it('reads a status that states no exit code, and no run, as completed', () => {
    expect(ampShellTaskResult('{"output":"x","running":false,"pid":1}', false)).toEqual({ outcome: 'completed', output: 'x' })
  })
})
