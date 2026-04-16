import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createTerminalStore } from '~/stores/terminal.store'

describe('createTerminalStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createTerminalStore()
      expect(store.state.terminals).toEqual([])
      expect(store.state.activeTerminalId).toBeNull()
      dispose()
    })
  })

  it('should update terminal title', () => {
    createRoot((dispose) => {
      const store = createTerminalStore()
      store.addTerminal({ id: 't1', workspaceId: 'ws1' })
      store.updateTerminalTitle('t1', 'bash')
      expect(store.state.terminals[0].title).toBe('bash')
      dispose()
    })
  })

  it('should set active terminal', () => {
    createRoot((dispose) => {
      const store = createTerminalStore()
      store.setActiveTerminal('t1')
      expect(store.state.activeTerminalId).toBe('t1')
      dispose()
    })
  })

  it('should upsert terminal metadata without changing the active terminal', () => {
    createRoot((dispose) => {
      const store = createTerminalStore()
      store.addTerminal({ id: 't1', workspaceId: 'ws1' })
      store.upsertTerminal({ id: 't2', workspaceId: 'ws1', title: 'restored' })
      store.upsertTerminal({ id: 't2', workspaceId: 'ws1', screen: new Uint8Array([1, 2, 3]) })

      expect(store.state.activeTerminalId).toBe('t1')
      expect(store.state.terminals).toHaveLength(2)
      expect(store.state.terminals[1].title).toBe('restored')
      expect(store.state.terminals[1].screen).toEqual(new Uint8Array([1, 2, 3]))
      dispose()
    })
  })
})
