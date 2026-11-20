import type { SessionMetadataDelivery } from '~/stores/agentSession.store'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, AgentStatusChangeSchema, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { handleAgentInactive, handleAgentSessionInfo, handleResultDivider } from '~/hooks/agentEvents'
import { clearOfflineAgentState } from '~/hooks/useWorkspaceConnection'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import '~/components/chat/providers'

const AGENT_ID = 'live-retry-agent'
const WORKSPACE_ID = 'live-retry-workspace'
const RUNNING = { phase: 'running', attempt: 2 }
const WAITING = {
  phase: 'waiting',
  attempt: 2,
  max_attempts: 5,
  retry_delay_ms: 0,
}

function message(content: unknown) {
  return create(AgentChatMessageSchema, {
    id: 'retry-event',
    source: MessageSource.AGENT,
    agentProvider: AgentProvider.ZCODE,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: -1n,
  })
}

function withRetryStores(body: (rig: ReturnType<typeof retryStores>) => void) {
  createRoot((dispose) => {
    try {
      body(retryStores())
    }
    finally {
      dispose()
    }
  })
}

function retryStores() {
  installTestBridge({ workspaceId: WORKSPACE_ID })
  const stores = {
    ...createTestTabStores(WORKSPACE_ID),
    agentSessionStore: createAgentSessionStore(),
    chatStore: createChatStore(),
    controlStore: createControlStore(),
    getActiveWorkspaceId: () => WORKSPACE_ID,
  }
  return {
    stores,
    receive(value: unknown, delivery?: SessionMetadataDelivery) {
      return handleAgentSessionInfo(AGENT_ID, parseMessageContent(message({
        type: 'agent_session_info',
        info: { model_api_retry: value },
      })), stores, delivery)
    },
    current: () => stores.agentSessionStore.getModelAPIRetry(AGENT_ID),
  }
}

describe('handleAgentSessionInfo model_api_retry', () => {
  it('keeps zero delay, omits absent fields, and applies explicit null', () => {
    withRetryStores(({ receive, current }) => {
      expect(receive(WAITING)).toBe(true)
      expect(current()).toEqual({
        phase: 'waiting',
        attempt: 2,
        maxAttempts: 5,
        retryDelayMs: 0,
      })
      receive(RUNNING)
      expect(current()).toEqual(RUNNING)
      receive(null)
      expect(current()).toBeUndefined()
    })
  })

  it('leaves the last accepted retry intact after a malformed update', () => {
    withRetryStores(({ receive, current }) => {
      receive(RUNNING)
      for (const value of [
        {},
        [],
        { phase: 'unknown', attempt: 3 },
        { phase: 'waiting', attempt: -1 },
        { phase: 'waiting', attempt: 2.5 },
        { phase: 'waiting', attempt: Number.MAX_SAFE_INTEGER + 1 },
        { phase: 'waiting', attempt: 3, max_attempts: 2 },
        { phase: 'waiting', attempt: 3, retry_delay_ms: -1 },
        { phase: 'running', attempt: 3, error_status: 99 },
      ]) {
        receive(value)
        expect(current()).toEqual(RUNNING)
      }
    })
  })

  it('clears a stale baseline and accepts the current full replay', () => {
    withRetryStores(({ stores, receive, current }) => {
      stores.agentSessionStore.setModelAPIRetry(AGENT_ID, { phase: 'waiting', attempt: 3 })
      stores.agentSessionStore.beginReplay(AGENT_ID, 1n)
      expect(current()).toBeUndefined()

      receive(RUNNING, { phase: 'replay', replayId: 1n })
      expect(current()).toEqual(RUNNING)

      stores.agentSessionStore.beginReplay(AGENT_ID, 2n)
      receive(RUNNING, { phase: 'replay', replayId: 1n })
      expect(current()).toBeUndefined()
    })
  })

  it('keeps a live value and clear ahead of replay delivery', () => {
    withRetryStores(({ stores, receive, current }) => {
      const replay: SessionMetadataDelivery = { phase: 'replay', replayId: 1n }
      stores.agentSessionStore.beginReplay(AGENT_ID, 1n)
      receive(RUNNING)
      receive(null, replay)
      receive(WAITING, replay)
      expect(current()).toEqual(RUNNING)
      receive(null)
      receive(WAITING, replay)
      expect(current()).toBeUndefined()
    })
  })

  it('keeps a newer retry when an earlier inactive status or live result arrives', () => {
    withRetryStores(({ stores, receive, current }) => {
      receive(RUNNING)
      handleAgentInactive(AGENT_ID, create(AgentStatusChangeSchema), 'catchingUp', stores)
      const result = message({
        type: 'turn.completed',
        payload: { resultType: 'success' },
      })
      handleResultDivider(AGENT_ID, result, parseMessageContent(result), stores, 'live')
      expect(current()).toEqual(RUNNING)
      handleAgentInactive(AGENT_ID, create(AgentStatusChangeSchema), 'live', stores)
      expect(current()).toBeUndefined()
      receive(RUNNING)
      clearOfflineAgentState(AGENT_ID, stores)
      expect(current()).toBeUndefined()
    })
  })
})
