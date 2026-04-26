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
