import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { PREFIX_EDITOR_DRAFT } from '~/lib/browserStorage'
import { clearDraft, loadDraft, saveDraft } from './draftPersistence'

const AGENT = 'agent-draft-1'

beforeEach(() => {
  localStorage.clear()
})
afterEach(() => {
  localStorage.clear()
})

describe('draftPersistence', () => {
  it('returns empty content and cursor=-1 when no draft is stored', () => {
    expect(loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
  })

  it('round-trips content and cursor through saveDraft / loadDraft', () => {
    saveDraft(AGENT, 'some draft text', 7)
    expect(loadDraft(AGENT)).toEqual({ content: 'some draft text', cursor: 7 })
  })

  it('persists drafts under a per-agent key prefix', () => {
    saveDraft('agent-a', 'a-content', 1)
    saveDraft('agent-b', 'b-content', 2)
    expect(loadDraft('agent-a').content).toBe('a-content')
    expect(loadDraft('agent-b').content).toBe('b-content')
    expect(localStorage.getItem(`${PREFIX_EDITOR_DRAFT}agent-a`)).not.toBeNull()
    expect(localStorage.getItem(`${PREFIX_EDITOR_DRAFT}agent-b`)).not.toBeNull()
  })

  it('saving an empty string removes the stored draft', () => {
    saveDraft(AGENT, 'something', 3)
    expect(loadDraft(AGENT).content).toBe('something')

    saveDraft(AGENT, '', -1)
    expect(loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
    expect(localStorage.getItem(`${PREFIX_EDITOR_DRAFT}${AGENT}`)).toBeNull()
  })

  it('clearDraft removes any persisted draft', () => {
    saveDraft(AGENT, 'persisted text', 5)
    clearDraft(AGENT)
    expect(loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
  })

  it('isolates drafts between agents on clear', () => {
    saveDraft('agent-a', 'a-content', 1)
    saveDraft('agent-b', 'b-content', 2)
    clearDraft('agent-a')
    expect(loadDraft('agent-a').content).toBe('')
    expect(loadDraft('agent-b').content).toBe('b-content')
  })
})
