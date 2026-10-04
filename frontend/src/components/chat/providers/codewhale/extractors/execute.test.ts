import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleCommandResult, codewhaleExecuteRequest } from './execute'

describe('codewhaleExecuteRequest', () => {
  it('reads the command, the description and the working directory', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.Bash, { command: 'ls', description: 'List', cwd: 'src' })).toStrictEqual({ command: 'ls', description: 'List', cwd: 'src' })
  })

  it('reads the text a terminal receives and the code a sandbox runs', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.TerminalSend, { input: 'y\n' }).command).toBe('y\n')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.CodeExecution, { code: 'print(1)' })).toStrictEqual({ command: 'print(1)' })
  })

  it('states the tool and its action when no argument states a command', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.Run, { action: 'verifiers' }).command).toBe('Run verifiers')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.PandocConvert, {}).command).toBe(CODEWHALE_TOOL.PandocConvert)
  })

  it('reads the command from each spelling, in order', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.LegacyBash, { cmd: 'make' }).command).toBe('make')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.Bash, { command: 'ls', cmd: 'make', code: 'x', input: 'y' }).command).toBe('ls')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.Bash, { command: '', cmd: 'make' }).command).toBe('make')
  })

  it('composes the Git command from the tool name, the facade\'s action and the path', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.GitDiff, { path: 'src' }).command).toBe('git diff src')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.GitBlame, { path: 'a.ts', action: 'ignored' }).command).toBe('git blame a.ts')
    // The facade with no action states the one true word it has.
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.Git, {}).command).toBe('git')
    // A Git tool never reads a `command` argument, which the runtime does not take.
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.GitLog, { command: 'rm -rf /' }).command).toBe('git log')
  })

  it('states the test runner with and without its extra arguments', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.RunTests, {}).command).toBe('cargo test')
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.RunTests, { args: '-p core' }).command).toBe('cargo test -p core')
  })

  it('marks JavaScript for the JavaScript sandbox alone', () => {
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.JsExecution, { code: '1 + 1' })).toStrictEqual({ command: '1 + 1', language: 'javascript' })
    expect(codewhaleExecuteRequest(CODEWHALE_TOOL.CodeExecution, { code: '1 + 1' })).toStrictEqual({ command: '1 + 1' })
  })
})

describe('codewhaleCommandResult', () => {
  it('reads the exit code and the duration', () => {
    expect(codewhaleCommandResult('out', { exit_code: 1, duration_ms: 0 })).toStrictEqual({ output: 'out', exitCode: 1, durationMs: 0 })
  })

  // A process that a signal ended reports a negative code, and a long command a
  // large duration. Both are real reports.
  it('keeps a negative exit code and a very large duration', () => {
    expect(codewhaleCommandResult('killed', { exit_code: -9, duration_ms: 86_400_000 })).toStrictEqual({ output: 'killed', exitCode: -9, durationMs: 86_400_000 })
  })

  // A background launch answers with a null code, because the job has not ended.
  it('states no exit code for a null one', () => {
    expect(codewhaleCommandResult('Background task started: shell_1', { exit_code: null, duration_ms: null })).toStrictEqual({ output: 'Background task started: shell_1' })
  })

  it('leaves out a code or a duration that is not a number the command could report', () => {
    expect(codewhaleCommandResult('out', {})).toStrictEqual({ output: 'out' })
    expect(codewhaleCommandResult('out', { exit_code: '1', duration_ms: -5 })).toStrictEqual({ output: 'out' })
    expect(codewhaleCommandResult('out', { exit_code: 1.5 })).toStrictEqual({ output: 'out' })
  })
})
