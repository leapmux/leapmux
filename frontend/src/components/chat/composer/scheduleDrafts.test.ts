import { createMemo, createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { queueEditDraftKey } from '../attachments'
import { clearScheduleDraft, forgetAgentScheduleDrafts, getScheduleDraft, setScheduleDraft } from './scheduleDrafts'

const KEYS = ['agent-1', 'agent-10', queueEditDraftKey('agent-1', 'input-1'), queueEditDraftKey('agent-10', 'input-1')]

afterEach(() => {
  for (const key of KEYS)
    clearScheduleDraft(key)
})

describe('scheduleDrafts', () => {
  it('holds no draft for a key that never received one', () => {
    expect(getScheduleDraft('agent-1')).toBeUndefined()
  })

  it('keeps a cleared schedule as a draft of its own, distinct from no draft', () => {
    setScheduleDraft('agent-1', '')
    expect(getScheduleDraft('agent-1')).toBe('')
    clearScheduleDraft('agent-1')
    expect(getScheduleDraft('agent-1')).toBeUndefined()
  })

  it('keeps the ordinary draft and an edit draft of one agent apart', () => {
    setScheduleDraft('agent-1', '2030-01-01T18:45')
    setScheduleDraft(queueEditDraftKey('agent-1', 'input-1'), '2030-01-02T18:45')
    expect(getScheduleDraft('agent-1')).toBe('2030-01-01T18:45')
    expect(getScheduleDraft(queueEditDraftKey('agent-1', 'input-1'))).toBe('2030-01-02T18:45')
  })

  it('notifies a reader of the key that changes', () => {
    createRoot((dispose) => {
      const draft = createMemo(() => getScheduleDraft('agent-1'))
      expect(draft()).toBeUndefined()
      setScheduleDraft('agent-1', '2030-01-01T18:45')
      expect(draft()).toBe('2030-01-01T18:45')
      clearScheduleDraft('agent-1')
      expect(draft()).toBeUndefined()
      dispose()
    })
  })

  it('forgets every draft of one agent and no draft of an agent whose id it prefixes', () => {
    for (const key of KEYS)
      setScheduleDraft(key, '2030-01-01T18:45')
    forgetAgentScheduleDrafts('agent-1')
    expect(getScheduleDraft('agent-1')).toBeUndefined()
    expect(getScheduleDraft(queueEditDraftKey('agent-1', 'input-1'))).toBeUndefined()
    expect(getScheduleDraft('agent-10')).toBe('2030-01-01T18:45')
    expect(getScheduleDraft(queueEditDraftKey('agent-10', 'input-1'))).toBe('2030-01-01T18:45')
  })

  it('forgets nothing for an agent that holds no draft', () => {
    setScheduleDraft('agent-10', '2030-01-01T18:45')
    forgetAgentScheduleDrafts('agent-1')
    expect(getScheduleDraft('agent-10')).toBe('2030-01-01T18:45')
  })
})
