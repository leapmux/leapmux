import { describe, expect, it } from 'vitest'
import { piSessionEntry } from './sessionEntry'

describe('piSessionEntry', () => {
  it('reads the original entry from its envelope', () => {
    const entry = { type: 'compaction', tokensBefore: 0 }
    expect(piSessionEntry({ type: 'entry_appended', entry })).toBe(entry)
  })

  it.each([{}, { type: 'message_end', entry: {} }, { type: 'entry_appended', entry: null }, { type: 'entry_appended', entry: [] }])('rejects an absent or malformed entry: %j', (message) => {
    expect(piSessionEntry(message)).toBeUndefined()
  })
})
