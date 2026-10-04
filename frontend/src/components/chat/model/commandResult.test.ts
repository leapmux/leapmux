import type { CommandResult } from './commandResult'
import { describe, expect, it } from 'vitest'
import { commandExit, commandIsError, withCommandExit } from './commandResult'

describe('commandExit', () => {
  it('keeps a code, a signal, or a failure with neither, alone', () => {
    expect(commandExit({ output: '', exitCode: 3 })).toEqual({ exitCode: 3 })
    expect(commandExit({ output: '', signal: 'killed' })).toEqual({ signal: 'killed' })
    expect(commandExit({ output: '', failed: true })).toEqual({ failed: true })
    expect(commandExit({ output: '' })).toEqual({})
    expect(commandExit({ output: '', exitCode: null })).toEqual({ exitCode: null })
  })

  // The type forbids the pairs, but a payload built through a cast can carry one.
  // A code or a signal states more than the bare failure, so it answers first.
  it('reads the code, then the signal, before a bare failure', () => {
    expect(commandExit({ output: '', exitCode: 3, failed: true } as unknown as CommandResult)).toEqual({ exitCode: 3 })
    expect(commandExit({ output: '', signal: 'killed', failed: true } as unknown as CommandResult)).toEqual({ signal: 'killed' })
  })
})

describe('commandIsError', () => {
  it('reports a non-zero code, a signal, and a failure with neither', () => {
    expect(commandIsError({ exitCode: 1 })).toBe(true)
    expect(commandIsError({ signal: 'killed' })).toBe(true)
    expect(commandIsError({ failed: true })).toBe(true)
  })

  it('reports no failure for a zero code, no code, or an unknown code', () => {
    expect(commandIsError({ exitCode: 0 })).toBe(false)
    expect(commandIsError({})).toBe(false)
    expect(commandIsError({ exitCode: null })).toBe(false)
  })
})

describe('withCommandExit', () => {
  // Each ending replaces whatever ending the result stated, so no result ever holds
  // two of them.
  it('replaces the ending and keeps the rest of the result', () => {
    const base = { output: 'out', label: 'make', durationMs: 5 }
    expect(withCommandExit({ ...base, signal: 'killed' }, { exitCode: 2 })).toEqual({ ...base, exitCode: 2 })
    expect(withCommandExit({ ...base, exitCode: 2 }, { signal: 'killed' })).toEqual({ ...base, signal: 'killed' })
    expect(withCommandExit({ ...base, failed: true }, { exitCode: 0 })).toEqual({ ...base, exitCode: 0 })
    expect(withCommandExit({ ...base, exitCode: 2 }, { failed: true })).toEqual({ ...base, failed: true })
    expect(withCommandExit({ ...base, exitCode: 2 }, {})).toEqual(base)
  })
})
