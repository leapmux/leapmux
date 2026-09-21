import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createAgentComposerActionStateStore } from './agentComposerActionState'

describe('createAgentComposerActionStateStore', () => {
  it('returns false when the retired agent has no stored state', () => {
    createRoot((dispose) => {
      const store = createAgentComposerActionStateStore()

      expect(store.releaseAgent('missing')).toBe(false)
      dispose()
    })
  })

  it('releases a retired agent and creates clean state if it returns', () => {
    createRoot((dispose) => {
      const store = createAgentComposerActionStateStore()
      const first = store.forAgent('agent-1')
      first.setEnqueueInFlight(true)
      first.setPauseInFlight(true)
      expect(store.releaseAgent('agent-1')).toBe(true)
      const revived = store.forAgent('agent-1')
      expect(revived).not.toBe(first)
      expect(revived.enqueueInFlight()).toBe(false)
      expect(revived.pauseInFlight()).toBe(false)
      dispose()
    })
  })
})
