import { describe, expect, it } from 'vitest'
import { acpTerminalFromToolCallContent } from './terminal'

describe('acpTerminalFromToolCallContent', () => {
  it('returns null for null/undefined/non-array', () => {
    expect(acpTerminalFromToolCallContent(null)).toBeNull()
    expect(acpTerminalFromToolCallContent(undefined)).toBeNull()
    expect(acpTerminalFromToolCallContent('nope')).toBeNull()
    expect(acpTerminalFromToolCallContent({})).toBeNull()
  })

  it('extracts the first terminal entry', () => {
    expect(acpTerminalFromToolCallContent([
      { type: 'content', content: { text: 'noise' } },
      { type: 'terminal', terminalId: 'term_abc' },
      { type: 'terminal', terminalId: 'term_later' },
    ])).toEqual({ terminalId: 'term_abc' })
  })

  it('skips terminal entries without a terminalId', () => {
    expect(acpTerminalFromToolCallContent([
      { type: 'terminal' },
      { type: 'terminal', terminalId: '' },
      { type: 'terminal', terminalId: 'term_ok' },
    ])).toEqual({ terminalId: 'term_ok' })
  })

  it('returns null when no terminal entry is present', () => {
    expect(acpTerminalFromToolCallContent([
      { type: 'diff', path: 'a.ts', oldText: '', newText: 'x' },
      { type: 'content', content: { text: 'hi' } },
    ])).toBeNull()
  })

  it('skips non-object entries in the content array', () => {
    expect(acpTerminalFromToolCallContent([
      null,
      'terminal',
      42,
      { type: 'terminal', terminalId: 'term_after_noise' },
    ])).toEqual({ terminalId: 'term_after_noise' })
  })
})
