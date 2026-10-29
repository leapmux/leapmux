import type { AgentControlRequest } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentControlRequestSchema, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { handleControlRequest } from '~/hooks/agentEvents'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

const WS = 'control-request-ws'

function controlStores() {
  const harness = installTestBridge({ workspaceId: WS })
  const tabs = createTestTabStores(WS)
  let nextPosition = 0
  return {
    agentSessionStore: createAgentSessionStore(),
    chatStore: createChatStore(),
    controlStore: createControlStore(),
    view: tabs.view,
    metadata: tabs.metadata,
    selection: tabs.selection,
    getActiveWorkspaceId: () => WS,
    /** Place an agent tab and write the worker-reported status beside it. */
    addAgent(id: string, agentStatus: AgentStatus) {
      nextPosition += 1
      emitAddTab({
        type: TabType.AGENT,
        id,
        tileId: harness.rootTileId,
        position: `p${nextPosition}`,
        workerId: 'w1',
      })
      tabs.metadata.patch(id, { agentStatus })
    },
  }
}

function request(overrides: { requestId: string, nonBlocking?: boolean }): AgentControlRequest {
  return create(AgentControlRequestSchema, {
    agentId: 'a1',
    requestId: overrides.requestId,
    claimToken: `tok-${overrides.requestId}`,
    payload: new TextEncoder().encode(JSON.stringify({
      request: {
        tool_name: 'CodexAsyncQuestion',
        input: { title: 'Which color?' },
      },
    })),
    ...(overrides.nonBlocking
      ? { nonBlocking: true }
      : {}),
  })
}

describe('handleControlRequest', () => {
  // A question belongs to the conversation, not to the provider process. A
  // reader who returns later must find it even when the agent already stopped.
  it('keeps a non-blocking request of an INACTIVE agent during catch-up', () => {
    createRoot((dispose) => {
      const s = controlStores()
      s.addAgent('a1', AgentStatus.INACTIVE)
      handleControlRequest('a1', request({ requestId: 'q1', nonBlocking: true }), 'catchingUp', s)
      expect(s.controlStore.getRequests('a1').map(entry => entry.requestId)).toEqual(['q1'])
      dispose()
    })
  })

  // The provider of a blocking request no longer waits once it stopped, so its
  // request would hold the composer forever with nobody to answer.
  it('still drops a blocking request of an INACTIVE agent during catch-up', () => {
    createRoot((dispose) => {
      const s = controlStores()
      s.addAgent('a1', AgentStatus.INACTIVE)
      handleControlRequest('a1', request({ requestId: 'b1' }), 'catchingUp', s)
      expect(s.controlStore.getRequests('a1')).toEqual([])
      dispose()
    })
  })

  it('maps the non-blocking flag from the proto event', () => {
    createRoot((dispose) => {
      const s = controlStores()
      s.addAgent('a1', AgentStatus.ACTIVE)
      handleControlRequest('a1', request({ requestId: 'q1', nonBlocking: true }), 'live', s)
      handleControlRequest('a1', request({ requestId: 'b1' }), 'live', s)
      const byId = new Map(s.controlStore.getRequests('a1').map(entry => [entry.requestId, entry]))
      expect(byId.get('q1')?.nonBlocking).toBe(true)
      // Absent means blocking: the zero value must stay the blocking request.
      expect(byId.get('b1')?.nonBlocking).toBeUndefined()
      dispose()
    })
  })
})
