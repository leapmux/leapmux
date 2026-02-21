import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import { createStore } from 'solid-js/store'

interface WorkspaceStoreState {
  workspaces: Workspace[]
  loading: boolean
  error: string | null
}

export function createWorkspaceStore() {
  const [state, setState] = createStore<WorkspaceStoreState>({
    workspaces: [],
    loading: false,
    error: null,
  })

  return {
    state,

    setWorkspaces(workspaces: Workspace[]) {
      setState('workspaces', workspaces)
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    setError(error: string | null) {
      setState('error', error)
    },

    addWorkspace(workspace: Workspace) {
      setState('workspaces', prev => [workspace, ...prev])
    },

    removeWorkspace(id: string) {
      setState('workspaces', prev => prev.filter(s => s.id !== id))
    },

    updateWorkspace(id: string, updates: Partial<Workspace>) {
      setState('workspaces', s => s.id === id, updates)
    },
  }
}
