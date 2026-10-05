import { execFileSync, spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { printfMarkerCommand, quotePosixShellArgument } from './shellArguments'

/** A Windows host has no /bin/sh. The cases that run the command in a shell need one. */
const hasPosixShell = existsSync('/bin/sh')

describe('quotePosixShellArgument', () => {
  it.each([
    { value: '', expected: '\'\'' },
    { value: 'one argument', expected: '\'one argument\'' },
    { value: 'one\'quote', expected: '\'one\'"\'"\'quote\'' },
    { value: '$(printf WRONG); `echo WRONG` *', expected: '\'$(printf WRONG); `echo WRONG` *\'' },
    { value: 'first\nsecond', expected: '\'first\nsecond\'' },
  ])('preserves the exact argument $value', ({ value, expected }) => {
    expect(quotePosixShellArgument(value)).toBe(expected)
  })
})

describe('printfMarkerCommand', () => {
  it.runIf(hasPosixShell).each([
    { prefix: 'SHELL0123456789abcdef', value: 42, output: 'SHELL0123456789abcdef42\n' },
    { prefix: 'SHELLERR_x-y', value: 77, output: 'SHELLERR_x-y77\n' },
    { prefix: 'BYPASS', value: 0, output: 'BYPASS0\n' },
    { prefix: 'NEGATIVE', value: -7, output: 'NEGATIVE-7\n' },
  ])('prints $output only when a POSIX shell runs it', ({ prefix, value, output }) => {
    const command = printfMarkerCommand(prefix, value)
    expect(command).not.toContain(output.trimEnd())
    expect(execFileSync('/bin/sh', ['-c', command], { encoding: 'utf8' })).toBe(output)
  })

  it('holds no syntax that Gemini CLI refuses as command substitution', () => {
    const command = printfMarkerCommand('SHELL0123456789abcdef', 42)
    expect(command).not.toMatch(/\$\(|`|[<>]\(/)
  })

  it.runIf(hasPosixShell)('keeps its output on the stream that the caller redirects to', () => {
    const result = spawnSync('/bin/sh', ['-c', `${printfMarkerCommand('SHELLERR', 77)} >&2; exit 7`], { encoding: 'utf8' })
    expect(result.status).toBe(7)
    expect(result.stdout).toBe('')
    expect(result.stderr).toBe('SHELLERR77\n')
  })

  it.each([
    { prefix: '', reason: 'an empty prefix' },
    { prefix: 'quote\'s', reason: 'a quote that ends the format' },
    { prefix: 'percent%d', reason: 'a printf directive' },
    { prefix: 'back\\slash', reason: 'a printf escape' },
    { prefix: 'two words', reason: 'a space' },
    { prefix: '-lead', reason: 'a leading hyphen that printf reads as an option' },
  ])('refuses $reason in the prefix', ({ prefix }) => {
    expect(() => printfMarkerCommand(prefix, 42)).toThrow('The printf marker prefix requires')
  })

  it.each([
    { value: 4.2 },
    { value: Number.NaN },
    { value: Number.POSITIVE_INFINITY },
    { value: Number.MAX_SAFE_INTEGER + 1 },
  ])('refuses the value $value', ({ value }) => {
    expect(() => printfMarkerCommand('SHELL', value)).toThrow('The printf marker value requires a safe integer.')
  })
})
