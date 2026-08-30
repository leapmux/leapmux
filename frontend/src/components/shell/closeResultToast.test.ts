/// <reference types="vitest/globals" />
import type { CloseTabResult } from '~/generated/proto/leapmux/v1/common_pb'
import { describe, expect, it } from 'vitest'
import { WorktreeRemovalOutcome } from '~/generated/proto/leapmux/v1/common_pb'
import { summarizeWorktreeCloses, worktreeRemovalToast } from './closeResultToast'

// The fold and the message it feeds. Both worktree-removal surfaces read them
// -- the Delete branch dialog for a whole branch group, and the last-tab close
// for one tab -- so the copy names the WORKTREE and never the tabs.

describe('summarizeWorktreeCloses', () => {
  const r = (worktreeRemoval: WorktreeRemovalOutcome) => ({ worktreeRemoval } as CloseTabResult)

  it('folds one worker outcome into its flag', () => {
    expect(summarizeWorktreeCloses([r(WorktreeRemovalOutcome.REMOVED)]))
      .toEqual({ removed: true, failed: false, stillReferenced: false, unknown: false })
    expect(summarizeWorktreeCloses([r(WorktreeRemovalOutcome.STILL_REFERENCED)]))
      .toEqual({ removed: false, failed: false, stillReferenced: true, unknown: false })
  })

  it('treats a missing result as UNKNOWN, not as a clean no-op', () => {
    // awaitCloseResult resolves undefined when the close RPC rejects, there was
    // no worker to reach, or the local close threw. A worker-reported outcome
    // is always a CloseTabResult -- even a degraded-to-KEEP close returns one
    // with UNSPECIFIED -- so a missing result genuinely means "we do not know".
    expect(summarizeWorktreeCloses([undefined]))
      .toEqual({ removed: false, failed: false, stillReferenced: false, unknown: true })
    // UNSPECIFIED is the definitive no-op, and must NOT read as unknown.
    expect(summarizeWorktreeCloses([r(WorktreeRemovalOutcome.UNSPECIFIED)]))
      .toEqual({ removed: false, failed: false, stillReferenced: false, unknown: false })
  })

  it('combines a mixed group with OR rather than picking one verdict', () => {
    expect(summarizeWorktreeCloses([r(WorktreeRemovalOutcome.REMOVED), undefined, r(WorktreeRemovalOutcome.FAILED)]))
      .toEqual({ removed: true, failed: true, stillReferenced: false, unknown: true })
  })

  it('reports nothing for an empty group', () => {
    expect(summarizeWorktreeCloses([]))
      .toEqual({ removed: false, failed: false, stillReferenced: false, unknown: false })
  })
})

describe('worktreeRemovalToast', () => {
  // Fill the summary defaults so each case sets only the flags it exercises.
  const s = (o: Partial<{ removed: boolean, failed: boolean, stillReferenced: boolean, unknown: boolean }> = {}) =>
    ({ removed: false, failed: false, stillReferenced: false, unknown: false, ...o })

  it('removed wins over everything, including a stale untracked snapshot and a sibling failure', () => {
    expect(worktreeRemovalToast(s({ removed: true, failed: true, stillReferenced: true, unknown: true }), false)).toBe('Worktree removed')
    expect(worktreeRemovalToast(s({ removed: true }), true)).toBe('Worktree removed')
  })

  it('failed stays silent — the close pipeline already warn-toasted its detail', () => {
    expect(worktreeRemovalToast(s({ failed: true }), true)).toBeNull()
    // failed outranks still-referenced, unknown, and the untracked snapshot.
    expect(worktreeRemovalToast(s({ failed: true, stillReferenced: true, unknown: true }), false)).toBeNull()
  })

  it('still-referenced wins over unknown and a stale untracked snapshot (only a tracked worktree can report it)', () => {
    expect(worktreeRemovalToast(s({ stillReferenced: true, unknown: true }), false))
      .toBe('Worktree still in use elsewhere')
    expect(worktreeRemovalToast(s({ stillReferenced: true }), true))
      .toBe('Worktree still in use elsewhere')
  })

  it('unknown (RPC rejected / unreachable / threw) reports "could not confirm", outranking the inspect snapshot', () => {
    // No definitive verdict from any tab: don't claim removed OR not-removed.
    // Wins over both the untracked snapshot and the tracked "not removed".
    expect(worktreeRemovalToast(s({ unknown: true }), false))
      .toBe('Could not confirm the worktree removal')
    expect(worktreeRemovalToast(s({ unknown: true }), true))
      .toBe('Could not confirm the worktree removal')
  })

  it('untracked snapshot with no other outcome reports "not tracked"', () => {
    expect(worktreeRemovalToast(s(), false))
      .toBe('Worktree kept: LeapMux does not track it')
  })

  it('tracked but nothing removed reports "not removed" (e.g. a startup-race strand the GC will reclaim)', () => {
    expect(worktreeRemovalToast(s(), true))
      .toBe('Worktree not removed')
  })
})
