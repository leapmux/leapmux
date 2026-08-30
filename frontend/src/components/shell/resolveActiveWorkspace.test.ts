import type { ResolveActiveWorkspaceArgs } from './resolveActiveWorkspace'
/// <reference types="vitest/globals" />
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { WorkspaceSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { resolveActiveWorkspace } from './resolveActiveWorkspace'

function ws(id: string): Workspace {
  return create(WorkspaceSchema, { id, title: id })
}

// Defaults to loaded: every case below describes a state AFTER a load attempt
// finished. The never-loaded state has its own case.
function stateOf(init: { workspaces?: Workspace[], loading?: boolean, error?: string | null, loaded?: boolean } = {}) {
  const store = createWorkspaceStore()
  store.setWorkspaces(init.workspaces ?? [])
  store.setLoading(init.loading ?? false)
  store.setError(init.error ?? null)
  if (init.loaded ?? true)
    store.markLoaded()
  return store.state
}

function resolve(args: Partial<ResolveActiveWorkspaceArgs> & { workspaceState: ResolveActiveWorkspaceArgs['workspaceState'] }) {
  return resolveActiveWorkspace({
    activeWorkspaceId: args.activeWorkspaceId ?? null,
    userId: args.userId ?? 'u1',
    savedWorkspaceId: args.savedWorkspaceId,
    workspaceState: args.workspaceState,
  })
}

describe('resolveActiveWorkspace', () => {
  describe('adopting a workspace when none is active', () => {
    it('adopts the saved workspace when it is still in the list', () => {
      expect(resolve({
        activeWorkspaceId: null,
        savedWorkspaceId: 'w2',
        workspaceState: stateOf({ workspaces: [ws('w1'), ws('w2')] }),
      })).toEqual({ kind: 'adopt', workspaceId: 'w2' })
    })

    it('adopts the first workspace when there is no saved id', () => {
      expect(resolve({
        activeWorkspaceId: null,
        workspaceState: stateOf({ workspaces: [ws('w1'), ws('w2')] }),
      })).toEqual({ kind: 'adopt', workspaceId: 'w1' })
    })

    // The saved id survives its workspace: another device can delete it while
    // this one is closed. Falling back to the first entry keeps the app usable
    // instead of reopening on nothing.
    it('adopts the first workspace when the saved id is no longer in the list', () => {
      expect(resolve({
        activeWorkspaceId: null,
        savedWorkspaceId: 'w-gone',
        workspaceState: stateOf({ workspaces: [ws('w1'), ws('w2')] }),
      })).toEqual({ kind: 'adopt', workspaceId: 'w1' })
    })

    // Nothing to adopt and nothing selected: the shell is already showing the
    // "Create a new workspace..." empty state, so there is no work to do.
    it('keeps when a completed load returned an empty list and nothing is active', () => {
      expect(resolve({
        activeWorkspaceId: null,
        workspaceState: stateOf({ workspaces: [] }),
      })).toEqual({ kind: 'keep' })
    })
  })

  describe('reacting to the active workspace disappearing', () => {
    it('keeps the active workspace while it is still in the list', () => {
      expect(resolve({
        activeWorkspaceId: 'w1',
        workspaceState: stateOf({ workspaces: [ws('w1')] }),
      })).toEqual({ kind: 'keep' })
    })

    // A delete from another device (or another browser tab) reaches this one as
    // a lifecycle event that reloads the list. Before the URL was removed this
    // rendered a dead-end 404; now it moves the user to a sibling.
    it('adopts a sibling when the active workspace was deleted elsewhere', () => {
      expect(resolve({
        activeWorkspaceId: 'w-deleted',
        savedWorkspaceId: 'w-deleted',
        workspaceState: stateOf({ workspaces: [ws('w1'), ws('w2')] }),
      })).toEqual({ kind: 'adopt', workspaceId: 'w1' })
    })

    // Last workspace gone: there is no sibling to fall back to, so the dead
    // selection has to be dropped or the shell keeps rendering a workspace that
    // no longer exists and the empty state never appears.
    it('clears when the active workspace was the last one', () => {
      expect(resolve({
        activeWorkspaceId: 'w-deleted',
        workspaceState: stateOf({ workspaces: [] }),
      })).toEqual({ kind: 'clear' })
    })
  })

  // Each guard below answers `keep` so an incomplete or failed load cannot move
  // the user. The control case in the block above ("clears when the active
  // workspace was the last one") is what stops these from passing against a
  // predicate that simply never decides anything.
  describe('states where the answer is not yet known', () => {
    it('keeps while the list is still loading', () => {
      expect(resolve({
        activeWorkspaceId: 'w-missing',
        workspaceState: stateOf({ workspaces: [ws('w1')], loading: true }),
      })).toEqual({ kind: 'keep' })
    })

    it('keeps before the user is restored', () => {
      expect(resolve({
        activeWorkspaceId: 'w-missing',
        userId: '',
        workspaceState: stateOf({ workspaces: [ws('w1')] }),
      })).toEqual({ kind: 'keep' })
    })

    // The store's INITIAL state is { loading: false, workspaces: [] }, which is
    // shape-identical to "loaded, and you own nothing". useWorkspaceLoader only
    // sets loading inside onMount, which Solid defers past the first render.
    it('keeps before any load has completed, even though the list is empty', () => {
      expect(resolve({
        activeWorkspaceId: 'w1',
        workspaceState: stateOf({ workspaces: [], loaded: false }),
      })).toEqual({ kind: 'keep' })
    })

    // A rejected listWorkspaces leaves the list empty, indistinguishable by
    // shape from "you own nothing". Clearing there drops the owner of a live
    // workspace onto the create-a-workspace empty state on a transient blip.
    it('keeps when the load failed, even though the list is empty', () => {
      expect(resolve({
        activeWorkspaceId: 'w1',
        workspaceState: stateOf({ workspaces: [], error: 'hub unavailable' }),
      })).toEqual({ kind: 'keep' })
    })

    // Same failed load, but the list came back non-empty from an earlier
    // success. The error still wins: a partial or stale list is not evidence
    // that the active workspace is gone.
    it('keeps when the load failed even though the stale list lacks the active workspace', () => {
      expect(resolve({
        activeWorkspaceId: 'w-missing',
        workspaceState: stateOf({ workspaces: [ws('w1')], error: 'hub unavailable' }),
      })).toEqual({ kind: 'keep' })
    })
  })
})
