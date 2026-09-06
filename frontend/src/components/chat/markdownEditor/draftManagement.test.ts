import type { Draft } from '~/lib/editor/draftPersistence'
import { describe, expect, it } from 'vitest'
import { flushStorageWrites } from '~/lib/browserStorage'
import { saveDraft } from '~/lib/editor/draftPersistence'
import { useTestStorage } from '~/test-support/persistentStorage'
import { createDraftSwapper } from './draftManagement'

// Drafts are on the ASYNCHRONOUS storage tier -- arbitrary user prose, so the
// family is unbounded and deliberately unmirrored. These swaps therefore reach
// a real database.
useTestStorage()

describe('createDraftSwapper', () => {
  it('applies the draft saved under the key it was given', async () => {
    saveDraft('agent-a', 'the first document', 4)
    await flushStorageWrites()

    const applied: Draft[] = []
    await createDraftSwapper()('agent-a', draft => applied.push(draft))

    expect(applied).toEqual([{ content: 'the first document', cursor: 4 }])
  })

  it('applies an empty document for a key that has no saved draft', async () => {
    const applied: Draft[] = []
    await createDraftSwapper()('agent-never-typed-in', draft => applied.push(draft))

    expect(applied).toEqual([{ content: '', cursor: -1 }])
  })

  // A null key is "no draft to restore" -- a control request with no draft
  // scope. It must still clear the outgoing document rather than leave the
  // previous key's prose on screen.
  it('applies an empty document for a null key, without a read', async () => {
    const applied: Draft[] = []
    await createDraftSwapper()(null, draft => applied.push(draft))

    expect(applied).toEqual([{ content: '', cursor: -1 }])
  })

  // THE GUARD. Two swaps in flight at once is the ordinary case for a user who
  // clicks through tabs faster than a read completes. Without the token the
  // reads land in whatever order the database answers, and the LOSER can be the
  // one that runs last -- installing the outgoing key's prose over the document
  // the user is now looking at, under a key that no longer matches it.
  it('ignores a read that a newer swap superseded', async () => {
    saveDraft('agent-a', 'the older document', 1)
    saveDraft('agent-b', 'the newer document', 2)
    await flushStorageWrites()

    const swap = createDraftSwapper()
    const applied: string[] = []
    // Started together, so both reads are in flight before either resolves.
    await Promise.all([
      swap('agent-a', draft => applied.push(draft.content)),
      swap('agent-b', draft => applied.push(draft.content)),
    ])

    expect(applied).toEqual(['the newer document'])
  })

  // The null arm takes no `await`, so it applies inside the caller's own turn
  // -- ahead of any read still in flight. It must still claim the token: a
  // control request with no draft scope arriving during a swap is exactly how
  // the outgoing key's prose would land in an editor that should be empty.
  it('lets a null key supersede a read that is still in flight', async () => {
    saveDraft('agent-a', 'the outgoing document', 1)
    await flushStorageWrites()

    const swap = createDraftSwapper()
    const applied: string[] = []
    await Promise.all([
      swap('agent-a', draft => applied.push(draft.content)),
      swap(null, draft => applied.push(draft.content)),
    ])

    expect(applied).toEqual([''])
  })

  // Each editor owns its own swapper, so one editor's swap must not cancel
  // another's. A token shared at module scope would make two open composers
  // silently blank each other.
  it('keeps two editors\' swaps independent', async () => {
    saveDraft('agent-a', 'left composer', 1)
    saveDraft('agent-b', 'right composer', 2)
    await flushStorageWrites()

    const left = createDraftSwapper()
    const right = createDraftSwapper()
    const applied: string[] = []
    await Promise.all([
      left('agent-a', draft => applied.push(draft.content)),
      right('agent-b', draft => applied.push(draft.content)),
    ])

    expect(applied.sort()).toEqual(['left composer', 'right composer'])
  })
})
