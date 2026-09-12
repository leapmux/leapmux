import { describe, expect, it } from 'vitest'
import { acpTerminalIds } from './terminal'

describe('acpTerminalIds', () => {
  it('returns an empty list for null/undefined/non-array', () => {
    expect(acpTerminalIds(null)).toEqual([])
    expect(acpTerminalIds(undefined)).toEqual([])
    expect(acpTerminalIds('nope')).toEqual([])
    expect(acpTerminalIds({})).toEqual([])
  })

  it('extracts every terminal entry', () => {
    expect(acpTerminalIds([
      { type: 'content', content: { text: 'noise' } },
      { type: 'terminal', terminalId: 'term_abc' },
      { type: 'terminal', terminalId: 'term_later' },
    ])).toEqual(['term_abc', 'term_later'])
  })

  it('skips terminal entries without a terminalId', () => {
    expect(acpTerminalIds([
      { type: 'terminal' },
      { type: 'terminal', terminalId: '' },
      { type: 'terminal', terminalId: 'term_ok' },
    ])).toEqual(['term_ok'])
  })

  it('returns an empty list when no terminal entry is present', () => {
    expect(acpTerminalIds([
      { type: 'diff', path: 'a.ts', oldText: '', newText: 'x' },
      { type: 'content', content: { text: 'hi' } },
    ])).toEqual([])
  })

  it('skips non-object entries in the content array', () => {
    expect(acpTerminalIds([
      null,
      'terminal',
      42,
      { type: 'terminal', terminalId: 'term_after_noise' },
    ])).toEqual(['term_after_noise'])
  })
})
