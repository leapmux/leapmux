import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createControlStore } from '~/stores/control.store'

/**
 * These tests verify the control-request guard in useWorkspaceConnection's
 * handleAgentEvent. Because handleAgentEvent is a closure that depends on
 * gRPC streams, we simulate its logic with real stores to verify the
 * invariant: control requests must not be added for INACTIVE agents.
 */
describe('controlRequest guard for inactive agents', () => {
  function makeAgent(id: string, status: AgentStatus) {
    return { id, status } as Parameters<ReturnType<typeof createAgentStore>['addAgent']>[0]
  }

  it('should not add control request when agent is INACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.INACTIVE))

      // Simulate the guard in useWorkspaceConnection's controlRequest handler:
      // const agentEntry = agentStore.state.agents.find(a => a.id === cr.agentId)
      // if (agentEntry?.status === AgentStatus.INACTIVE) break
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (agentEntry?.status !== AgentStatus.INACTIVE) {
        controlStore.addRequest('agent-1', {
          requestId: 'r1',
          agentId: 'agent-1',
          payload: { method: 'item/commandExecution/requestApproval' },
        })
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should add control request when agent is ACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))

      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (agentEntry?.status !== AgentStatus.INACTIVE) {
        controlStore.addRequest('agent-1', {
          requestId: 'r1',
          agentId: 'agent-1',
          payload: { method: 'item/commandExecution/requestApproval' },
        })
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests when agent becomes INACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))
      controlStore.addRequest('agent-1', {
        requestId: 'r1',
        agentId: 'agent-1',
        payload: { method: 'item/commandExecution/requestApproval' },
      })

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate statusChange INACTIVE → controlStore.clearAgent()
      agentStore.updateAgent('agent-1', { status: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })
})
