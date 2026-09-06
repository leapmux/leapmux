import { describe, expect, it } from 'vitest'
import { localStorageLoad, PREFIX_EDITOR_DRAFT } from '~/lib/browserStorage'
import { useTestStorage } from '~/test-support/persistentStorage'
import { clearDraft, loadDraft, saveDraft } from './draftPersistence'

// Drafts are on the ASYNCHRONOUS storage tier -- they are arbitrary user prose,
// so the family is unbounded and is deliberately not mirrored in memory. These
// round-trips therefore need a database to round-trip through.
useTestStorage()

const AGENT = 'agent-draft-1'

describe('draftPersistence', () => {
  it('returns empty content and cursor=-1 when no draft is stored', async () => {
    expect(await loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
  })

  it('round-trips content and cursor through saveDraft / loadDraft', async () => {
    saveDraft(AGENT, 'some draft text', 7)
    expect(await loadDraft(AGENT)).toEqual({ content: 'some draft text', cursor: 7 })
  })

  it('persists drafts under a per-agent key', async () => {
    saveDraft('agent-a', 'a-content', 1)
    saveDraft('agent-b', 'b-content', 2)
    expect((await loadDraft('agent-a')).content).toBe('a-content')
    expect((await loadDraft('agent-b')).content).toBe('b-content')
    // Through the gateway rather than by poking the store: the draft lives in
    // IndexedDB under a composed key, and naming that layout here is what goes
    // stale the moment it changes.
    expect(await localStorageLoad(`${PREFIX_EDITOR_DRAFT}agent-a`)).toBeDefined()
    expect(await localStorageLoad(`${PREFIX_EDITOR_DRAFT}agent-b`)).toBeDefined()
  })

  it('saving an empty string removes the stored draft', async () => {
    saveDraft(AGENT, 'something', 3)
    expect((await loadDraft(AGENT)).content).toBe('something')

    saveDraft(AGENT, '', -1)
    expect(await loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
    expect(await localStorageLoad(`${PREFIX_EDITOR_DRAFT}${AGENT}`)).toBeUndefined()
  })

  it('clearDraft removes any persisted draft', async () => {
    saveDraft(AGENT, 'persisted text', 5)
    clearDraft(AGENT)
    expect(await loadDraft(AGENT)).toEqual({ content: '', cursor: -1 })
  })

  it('isolates drafts between agents on clear', async () => {
    saveDraft('agent-a', 'a-content', 1)
    saveDraft('agent-b', 'b-content', 2)
    clearDraft('agent-a')
    expect((await loadDraft('agent-a')).content).toBe('')
    expect((await loadDraft('agent-b')).content).toBe('b-content')
  })
})
