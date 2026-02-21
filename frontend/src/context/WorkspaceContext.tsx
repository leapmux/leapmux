import type { ParentComponent } from 'solid-js'
import { createContext, createSignal, useContext } from 'solid-js'

interface WorkspaceState {
  activeWorkspaceId: () => string | null
  setActiveWorkspaceId: (id: string | null) => void
}

const WorkspaceContext = createContext<WorkspaceState>()

export const WorkspaceProvider: ParentComponent = (props) => {
  const [activeWorkspaceId, setActiveWorkspaceId] = createSignal<string | null>(null)

  return (
    <WorkspaceContext.Provider value={{ activeWorkspaceId, setActiveWorkspaceId }}>
      {props.children}
    </WorkspaceContext.Provider>
  )
}

export function useWorkspace(): WorkspaceState {
  const ctx = useContext(WorkspaceContext)
  if (!ctx) {
    throw new Error('useWorkspace must be used within WorkspaceProvider')
  }
  return ctx
}
