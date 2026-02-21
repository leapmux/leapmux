import { createStore } from 'solid-js/store'

export interface TerminalInfo {
  id: string
  workspaceId: string
  workerId?: string
  workingDir?: string
  screen?: Uint8Array
  cols?: number
  rows?: number
  title?: string
  exited?: boolean
}

interface TerminalStoreState {
  terminals: TerminalInfo[]
  activeTerminalId: string | null
}

export function createTerminalStore() {
  const [state, setState] = createStore<TerminalStoreState>({
    terminals: [],
    activeTerminalId: null,
  })

  return {
    state,

    setTerminals(terminals: TerminalInfo[]) {
      setState('terminals', terminals)
    },

    addTerminal(terminal: TerminalInfo) {
      setState('terminals', prev => [...prev, terminal])
      setState('activeTerminalId', terminal.id)
    },

    removeTerminal(id: string) {
      setState('terminals', prev => prev.filter(t => t.id !== id))
      if (state.activeTerminalId === id) {
        setState('activeTerminalId', state.terminals[0]?.id ?? null)
      }
    },

    updateTerminalTitle(id: string, title: string) {
      setState('terminals', t => t.id === id, 'title', title)
    },

    hasTerminal(id: string) {
      return state.terminals.some(t => t.id === id)
    },

    markExited(id: string) {
      setState('terminals', t => t.id === id, 'exited', true)
    },

    isExited(id: string) {
      return state.terminals.find(t => t.id === id)?.exited === true
    },

    setActiveTerminal(id: string) {
      setState('activeTerminalId', id)
    },
  }
}
