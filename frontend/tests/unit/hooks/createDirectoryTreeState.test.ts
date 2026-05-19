import type { DirectoryTreeHandle } from '~/components/tree/DirectoryTree'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { flush } from '../helpers/async'

function makeHandle(): DirectoryTreeHandle & { refresh: ReturnType<typeof vi.fn> } {
  return {
    collapseAll: vi.fn(),
    refresh: vi.fn(),
  }
}

describe('createDirectoryTreeState', () => {
  it('starts with treeKey at 0', () => {
    createRoot((dispose) => {
      const state = createDirectoryTreeState()
      expect(state.treeKey()).toBe(0)
      dispose()
    })
  })

  it('refreshTree increments treeKey on each call', () => {
    createRoot((dispose) => {
      const state = createDirectoryTreeState()
      state.refreshTree()
      expect(state.treeKey()).toBe(1)
      state.refreshTree()
      state.refreshTree()
      expect(state.treeKey()).toBe(3)
      dispose()
    })
  })

  it('refreshTree calls handle.refresh() when a tree handle is set', () => {
    createRoot((dispose) => {
      const state = createDirectoryTreeState()
      const handle = makeHandle()
      state.setTreeRef(handle)
      state.refreshTree()
      expect(handle.refresh).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('refreshTree is safe when no tree handle has been registered (no throw, key still increments)', () => {
    createRoot((dispose) => {
      const state = createDirectoryTreeState()
      expect(() => state.refreshTree()).not.toThrow()
      // The treeKey still bumps so consumers (GitOptions.refreshKey)
      // re-fetch even when the tree itself isn't mounted yet.
      expect(state.treeKey()).toBe(1)
      dispose()
    })
  })

  it('does not auto-refresh after mount — DirectoryTree owns its own initial load', async () => {
    // The hook used to call treeHandle?.refresh() in onMount, but in
    // production the ref is bound by a `<Show>`-gated child whose ref
    // callback fires AFTER the parent's onMount, so the refresh was a
    // no-op in every real consumer. DirectoryTree's own createEffect
    // performs the initial fetch on mount; double-driving it from here
    // would duplicate the first round trip.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const state = createDirectoryTreeState()
        const handle = makeHandle()
        state.setTreeRef(handle)
        await flush()
        expect(handle.refresh).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('setTreeRef replaces the previous handle (later refresh hits only the new one)', () => {
    createRoot((dispose) => {
      const state = createDirectoryTreeState()
      const first = makeHandle()
      const second = makeHandle()
      state.setTreeRef(first)
      state.setTreeRef(second)
      state.refreshTree()
      expect(first.refresh).not.toHaveBeenCalled()
      expect(second.refresh).toHaveBeenCalledTimes(1)
      dispose()
    })
  })
})
