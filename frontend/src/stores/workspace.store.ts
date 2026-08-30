import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createStore } from 'solid-js/store'

interface WorkspaceStoreState {
  workspaces: Workspace[]
  loading: boolean
  error: string | null
  /**
   * Whether a load has ever COMPLETED (successfully or not).
   *
   * `loading` alone cannot express "never asked": it starts false, and the
   * loader only flips it inside onMount, which Solid defers past the first
   * render. So `{ loading: false, workspaces: [] }` is the initial state AND
   * the genuine "you own nothing" state, and a consumer that reads it as the
   * latter clears the selection of a workspace the user owns. See
   * resolveActiveWorkspace.
   */
  loaded: boolean
}

export function createWorkspaceStore() {
  const [state, setState] = createStore<WorkspaceStoreState>({
    workspaces: [],
    loading: false,
    error: null,
    loaded: false,
  })

  return {
    state,

    setWorkspaces(workspaces: Workspace[]) {
      setState('workspaces', workspaces)
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    /** Marks that a load attempt has completed; never goes back to false. */
    markLoaded() {
      setState('loaded', true)
    },

    setError(error: string | null) {
      setState('error', error)
    },

    removeWorkspace(id: string) {
      setState('workspaces', prev => prev.filter(s => s.id !== id))
    },

    updateWorkspace(id: string, updates: Partial<Workspace>) {
      setState('workspaces', s => s.id === id, updates)
    },
  }
}
