import type { AgentSettledEventDetail } from '~/lib/agentSettledEvent'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { handleActivityChanged } from '~/hooks/agentEvents'
import { AGENT_SETTLED_EVENT } from '~/lib/agentSettledEvent'
import { createAgentActivityStore } from '~/stores/agentActivity.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

const WORKSPACE_ID = 'settled-receipt-workspace'

function receiptStores(onAgentSettled: (agentId: string, toolUses?: number) => void) {
  const bridge = installTestBridge({ workspaceId: WORKSPACE_ID })
  const tabs = createTestTabStores(WORKSPACE_ID)
  for (const [index, id] of ['agent-1', 'agent-2'].entries()) {
    emitAddTab({ type: TabType.AGENT, id, tileId: bridge.rootTileId, position: `p${index + 1}`, workerId: 'worker-1' })
    tabs.metadata.patch(id, { agentStatus: AgentStatus.ACTIVE })
  }
  tabs.selection.setActiveById(TabType.AGENT, 'agent-1')
  return {
    view: tabs.view,
    metadata: tabs.metadata,
    selection: tabs.selection,
    getActiveWorkspaceId: () => WORKSPACE_ID,
    agentActivityStore: createAgentActivityStore(),
    onAgentSettled,
  }
}

describe('handleActivityChanged', () => {
  it('reports the settled edge after the state and sound callback update', () => {
    createRoot((dispose) => {
      const order: string[] = []
      const stores = receiptStores(() => order.push('sound'))
      const receipts: { detail: AgentSettledEventDetail, busy: boolean }[] = []
      const listener = (event: Event) => {
        order.push('receipt')
        const detail = (event as CustomEvent<AgentSettledEventDetail>).detail
        receipts.push({ detail, busy: stores.agentActivityStore.isBusy(detail.agentId) })
      }
      window.addEventListener(AGENT_SETTLED_EVENT, listener)
      try {
        handleActivityChanged('agent-1', { state: AgentActivityState.WORKING }, stores)
        expect(receipts).toEqual([])
        handleActivityChanged('agent-1', { state: AgentActivityState.IDLE, numToolUses: 0 }, stores)
        expect(order).toEqual(['sound', 'receipt'])
        expect(receipts).toEqual([{ detail: { agentId: 'agent-1', state: AgentActivityState.IDLE, numToolUses: 0 }, busy: false }])
      }
      finally {
        window.removeEventListener(AGENT_SETTLED_EVENT, listener)
        dispose()
      }
    })
  })

  it('keeps an absent tool count separate from explicit zero', () => {
    createRoot((dispose) => {
      const stores = receiptStores(vi.fn())
      const receipts: AgentSettledEventDetail[] = []
      const listener = (event: Event) => receipts.push((event as CustomEvent<AgentSettledEventDetail>).detail)
      window.addEventListener(AGENT_SETTLED_EVENT, listener)
      try {
        handleActivityChanged('agent-1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.IDLE }, stores)
        handleActivityChanged('agent-2', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('agent-2', { state: AgentActivityState.IDLE, numToolUses: 0 }, stores)
        expect(receipts).toEqual([
          { agentId: 'agent-1', state: AgentActivityState.IDLE },
          { agentId: 'agent-2', state: AgentActivityState.IDLE, numToolUses: 0 },
        ])
        expect(Object.hasOwn(receipts[0] ?? {}, 'numToolUses')).toBe(false)
        expect(Object.hasOwn(receipts[1] ?? {}, 'numToolUses')).toBe(true)
      }
      finally {
        window.removeEventListener(AGENT_SETTLED_EVENT, listener)
        dispose()
      }
    })
  })

  it('reports waiting edges but excludes startup and repeated idle reports', () => {
    createRoot((dispose) => {
      const stores = receiptStores(vi.fn())
      const receipts: AgentSettledEventDetail[] = []
      const listener = (event: Event) => receipts.push((event as CustomEvent<AgentSettledEventDetail>).detail)
      window.addEventListener(AGENT_SETTLED_EVENT, listener)
      try {
        stores.metadata.patch('agent-1', { agentStatus: AgentStatus.STARTING })
        handleActivityChanged('agent-1', { state: AgentActivityState.IDLE }, stores)
        expect(receipts).toEqual([])
        stores.metadata.patch('agent-1', { agentStatus: AgentStatus.ACTIVE })
        handleActivityChanged('agent-1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.WAITING_FOR_USER }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.WAITING_FOR_USER }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.IDLE, numToolUses: 1 }, stores)
        handleActivityChanged('agent-1', { state: AgentActivityState.IDLE, numToolUses: 1 }, stores)
        expect(receipts).toEqual([
          { agentId: 'agent-1', state: AgentActivityState.WAITING_FOR_USER },
          { agentId: 'agent-1', state: AgentActivityState.IDLE, numToolUses: 1 },
        ])
      }
      finally {
        window.removeEventListener(AGENT_SETTLED_EVENT, listener)
        dispose()
      }
    })
  })

  it('reports no receipt when the synchronous sound callback fails', () => {
    createRoot((dispose) => {
      const stores = receiptStores(() => {
        throw new Error('The sound callback failed.')
      })
      const listener = vi.fn()
      window.addEventListener(AGENT_SETTLED_EVENT, listener)
      try {
        handleActivityChanged('agent-1', { state: AgentActivityState.WORKING }, stores)
        expect(() => handleActivityChanged('agent-1', { state: AgentActivityState.IDLE }, stores)).toThrow('The sound callback failed.')
        expect(listener).not.toHaveBeenCalled()
      }
      finally {
        window.removeEventListener(AGENT_SETTLED_EVENT, listener)
        dispose()
      }
    })
  })
})
