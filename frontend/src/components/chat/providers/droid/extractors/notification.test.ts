import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { droidCompactionBoundary, droidNotificationEntry } from './notification'

function parsed(message: Record<string, unknown>): ParsedMessageContent {
  return { rawText: JSON.stringify(message), topLevel: message, parentObject: message, wrapper: null }
}

describe('droidCompactionBoundary', () => {
  it('reads a standalone native boundary', () => {
    expect(droidCompactionBoundary(parsed({ type: 'session_compacted', removedCount: 3 }))).toEqual({})
  })

  it('finds a boundary after another notice in a stored thread', () => {
    const wrapper = {
      type: 'notification_thread',
      old_seqs: [],
      messages: [{ type: 'settings_updated' }, { type: 'session_compacted', removedCount: 3 }],
    }
    expect(droidCompactionBoundary({
      rawText: JSON.stringify(wrapper),
      topLevel: wrapper,
      parentObject: wrapper,
      wrapper,
    })).toEqual({})
  })

  it('ignores a row with no compaction event', () => {
    expect(droidCompactionBoundary(parsed({ type: 'settings_updated' }))).toBeNull()
  })
})

describe('droidNotificationEntry', () => {
  it('hides bookkeeping but keeps an unknown native frame visible', () => {
    expect(droidNotificationEntry({ type: 'session_token_usage_changed', tokenUsage: { used: 12 } })).toEqual([])
    expect(droidNotificationEntry({ type: 'unknown_native_event', value: 1 })).toEqual([
      { kind: 'text', text: '{"type":"unknown_native_event","value":1}' },
    ])
  })

  it('shows native errors without losing a nested or unknown diagnostic', () => {
    expect(droidNotificationEntry({ type: 'error', message: 'The tool failed.' })).toEqual([
      { kind: 'text', text: 'The tool failed.' },
    ])
    expect(droidNotificationEntry({ type: 'error', error: { message: 'Nested failure.' } })).toEqual([
      { kind: 'text', text: 'Nested failure.' },
    ])
    expect(droidNotificationEntry({ type: 'error' })).toEqual([
      { kind: 'text', text: '{"type":"error"}' },
    ])
  })
})
