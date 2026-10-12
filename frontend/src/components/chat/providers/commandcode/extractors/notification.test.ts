import { describe, expect, it } from 'vitest'
import { commandCodeNotificationEntry } from './notification'

function event(type: string, fields: Record<string, unknown>) {
  return { type: 'event', event: { type, ...fields } }
}

describe('commandCodeNotificationEntry', () => {
  it('does not call a finished manual attempt a successful compaction', () => {
    expect(commandCodeNotificationEntry(event('compaction_done', { trigger: 'manual', tokensSaved: 0 }))).toEqual([])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { trigger: 'manual', outcome: 'failed' }))).toEqual([{ kind: 'text', text: 'Native compaction failed.' }])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { trigger: 'manual', outcome: 'too-small' }))).not.toContainEqual(expect.objectContaining({ kind: 'compaction', phase: 'end' }))
  })

  it('reads the successful native manual outcome even when the token savings equal zero', () => {
    expect(commandCodeNotificationEntry(event('compaction_outcome', { trigger: 'manual', outcome: 'summarized', tokensBefore: 1000, tokensAfter: 1000 }))).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual', pre: 1000, post: 1000 } }])
  })

  it('keeps a positive automatic trim boundary without duplicating a manual outcome', () => {
    expect(commandCodeNotificationEntry(event('compaction_done', { tokensSaved: 101 }))).toEqual([{ kind: 'compaction', phase: 'end', detail: {} }])
    expect(commandCodeNotificationEntry(event('compaction_done', { trigger: 'manual', tokensSaved: 101 }))).toEqual([])
  })
})
