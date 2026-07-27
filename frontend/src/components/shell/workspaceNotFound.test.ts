/// <reference types="vitest/globals" />
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { WorkspaceSchema } from '~/generated/leapmux/v1/workspace_pb'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { isWorkspaceNotFound } from './workspaceNotFound'

function ws(id: string): Workspace {
  return create(WorkspaceSchema, { id, title: id })
}

// Defaults to loaded: every case below describes a state AFTER a load attempt
// finished. The never-loaded state has its own case at the bottom.
function stateOf(init: { workspaces?: Workspace[], loading?: boolean, error?: string | null, loaded?: boolean } = {}) {
  const store = createWorkspaceStore()
  store.setWorkspaces(init.workspaces ?? [])
  store.setLoading(init.loading ?? false)
  store.setError(init.error ?? null)
  if (init.loaded ?? true)
    store.markLoaded()
  return store.state
}

describe('isWorkspaceNotFound', () => {
  it('is false on the home route', () => {
    expect(isWorkspaceNotFound({
      workspaceId: undefined,
      userId: 'u1',
      workspaceState: stateOf(),
    })).toBe(false)
  })

  it('is true when the loaded list does not contain the URL workspace', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w-missing',
      userId: 'u1',
      workspaceState: stateOf({ workspaces: [ws('w1')] }),
    })).toBe(true)
  })

  it('is false while the list is still loading', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w-missing',
      userId: 'u1',
      workspaceState: stateOf({ loading: true }),
    })).toBe(false)
  })

  it('is false before the user is restored', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w-missing',
      userId: '',
      workspaceState: stateOf(),
    })).toBe(false)
  })

  it('is false when the workspace is present', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w1',
      userId: 'u1',
      workspaceState: stateOf({ workspaces: [ws('w1')] }),
    })).toBe(false)
  })

  // The store's INITIAL state is { loading: false, workspaces: [] }, which is
  // shape-identical to "loaded, and you own nothing". useWorkspaceLoader only
  // sets loading inside onMount, which Solid defers past the first render, so
  // without the `loaded` bit the first evaluation on a /workspace/:id load
  // scores a genuine 404 for a workspace the user owns.
  it('is false before any load has completed, even though the list is empty', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w1',
      userId: 'u1',
      workspaceState: stateOf({ workspaces: [], loaded: false }),
    })).toBe(false)
  })

  // Control for the case above: the SAME empty list, once a load has finished,
  // is a real 404. Without this the assertion above would also pass against a
  // predicate that simply never returns true.
  it('is true when a completed load returned an empty list', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w1',
      userId: 'u1',
      workspaceState: stateOf({ workspaces: [] }),
    })).toBe(true)
  })

  // A rejected listWorkspaces leaves the list empty, which by shape alone is
  // indistinguishable from "you own nothing". Rendering the 404 there tells
  // the owner of a live workspace it "doesn't exist or you don't have access"
  // -- a dead end no in-app action clears.
  it('is false when the load failed, even though the list is empty', () => {
    expect(isWorkspaceNotFound({
      workspaceId: 'w1',
      userId: 'u1',
      workspaceState: stateOf({ workspaces: [], error: 'hub unavailable' }),
    })).toBe(false)
  })
})
