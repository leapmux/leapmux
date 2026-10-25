import { describe, expect, it } from 'vitest'
import { queueEditDraftKey, queueEditDraftKeyPrefix } from './draftIdentity'

describe('queueEditDraftKey', () => {
  it('composes the item key from the agent prefix', () => {
    expect(queueEditDraftKeyPrefix('agent-1')).toBe('agent-1-queue-')
    expect(queueEditDraftKey('agent-1', 'input-9')).toBe('agent-1-queue-input-9')
  })

  it('keeps neighboring agent ids and different input ids distinct', () => {
    expect(queueEditDraftKey('agent-1', 'input-1')).not.toBe(queueEditDraftKey('agent-10', 'input-1'))
    expect(queueEditDraftKey('agent-1', 'input-1')).not.toBe(queueEditDraftKey('agent-1', 'input-10'))
  })
})
