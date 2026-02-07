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
})
