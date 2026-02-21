import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createWorkspaceStore } from '~/stores/workspace.store'

describe('createWorkspaceStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createWorkspaceStore()
      expect(store.state.workspaces).toEqual([])
      expect(store.state.loading).toBe(false)
      expect(store.state.error).toBeNull()
      dispose()
    })
  })

  it('should set loading', () => {
    createRoot((dispose) => {
      const store = createWorkspaceStore()
      store.setLoading(true)
      expect(store.state.loading).toBe(true)
      dispose()
    })
  })

  it('should set error', () => {
    createRoot((dispose) => {
      const store = createWorkspaceStore()
      store.setError('something went wrong')
      expect(store.state.error).toBe('something went wrong')
      dispose()
    })
  })
})
