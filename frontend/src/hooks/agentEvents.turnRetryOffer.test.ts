import type { AgentMessageStores } from '~/hooks/agentEvents'
/// <reference types="vitest/globals" />
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearPerTurnLiveState, handleAgentSessionInfo, handleResultDivider } from '~/hooks/agentEvents'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import '~/components/chat/providers'

const OFFER_WIRE = {
  turn_id: 'turn-1',
  model: 'gpt-6-luna',
  learn_more_url: 'https://help.openai.com/en/articles/20001326',
}
const OFFER = {
  turnId: 'turn-1',
  model: 'gpt-6-luna',
  learnMoreUrl: 'https://help.openai.com/en/articles/20001326',
}

function agentMessage(content: unknown) {
  return create(AgentChatMessageSchema, {
    id: 'm1',
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    agentProvider: AgentProvider.CODEX,
    spanId: '',
    agentSessionId: 'sess-1',
  })
}

function sessionInfoMessage(info: unknown) {
  return agentMessage({ type: 'agent_session_info', info })
}

function stores(): Pick<AgentMessageStores, 'agentSessionStore' | 'chatStore'> {
  return { agentSessionStore: createAgentSessionStore(), chatStore: createChatStore() }
}

describe('handleAgentSessionInfo turn_retry_offer', () => {
  it('applies an offer to the chat store and consumes the message', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), s)).toBe(true)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })

  it('keeps the offer usable without the optional learn-more URL', () => {
    createRoot((dispose) => {
      const s = stores()
      const { learn_more_url: _omitted, ...withoutURL } = OFFER_WIRE
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: withoutURL })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual({
        turnId: 'turn-1',
        model: 'gpt-6-luna',
      })
      dispose()
    })
  })

  // The worker can replace an offer that gave a help page with one that gives
  // none. The banner must not keep the help page of the earlier notice.
  it('drops the learn-more URL of the earlier offer when a newer offer gives none', () => {
    createRoot((dispose) => {
      const s = stores()
      const { learn_more_url: _omitted, ...withoutURL } = OFFER_WIRE
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { ...withoutURL, turn_id: 'turn-2' } })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual({ turnId: 'turn-2', model: 'gpt-6-luna' })
      dispose()
    })
  })

  // The worker writes only the fields that the contract lists. A field that
  // the contract does not list must not reach the store.
  it('keeps only the contract fields of the offer', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { ...OFFER_WIRE, agent_session_id: 'sess-1' } })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })

  // The worker withdraws the offer with an explicit null. Only that value
  // clears it: an unreadable object leaves the live offer alone.
  it('withdraws an offer on a null value', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: null })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      dispose()
    })
  })

  it('ignores an offer that gives no turn or no model', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { turn_id: 'turn-1' } })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { model: 'gpt-6-luna' } })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: 42 })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      dispose()
    })
  })

  it('leaves another agent offer alone', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      handleAgentSessionInfo('a2', parseMessageContent(sessionInfoMessage({ turn_retry_offer: null })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })

  // The offer identifies one turn. A newer offer replaces the earlier one, so
  // the banner can never propose a turn that already ended.
  it('replaces the live offer with a newer one', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { ...OFFER_WIRE, turn_id: 'turn-2', model: 'gpt-6-nano' } })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual({ ...OFFER, turnId: 'turn-2', model: 'gpt-6-nano' })
      dispose()
    })
  })

  // Only an explicit null withdraws the live offer. An object that gives no
  // turn or no model carries no offer, so it must leave the live one alone.
  it('leaves the live offer alone when a later offer gives no turn or model', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      for (const value of [{ turn_id: 'turn-2' }, { model: 'gpt-6-nano' }, {}, 42, 'x'])
        handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: value })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })

  // A transcript-only frame keeps its row and bytes without effects on the
  // current state. It must neither set the offer nor replace the live one.
  it('does not set or replace the offer from a transcript-only frame', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s, { phase: 'live' }, true)
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: { ...OFFER_WIRE, turn_id: 'turn-2' } })), s, { phase: 'live' }, true)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })
})

describe('clearPerTurnLiveState turn_retry_offer', () => {
  // The offer identifies one turn. A turn boundary removes it together with
  // the other per-turn live state.
  it('withdraws the offer with the other per-turn state', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      clearPerTurnLiveState('a1', s)
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      dispose()
    })
  })
})

describe('the turn-retry offer is cleared at turn and agent boundaries', () => {
  const WS = 'ws-1'

  /** Supply the stores that a turn-end divider requires. */
  function boundaryStores(): AgentMessageStores {
    installTestBridge({ workspaceId: WS })
    const tabs = createTestTabStores(WS)
    return {
      agentSessionStore: createAgentSessionStore(),
      chatStore: createChatStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
    }
  }

  it('withdraws the offer at a live turn end', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      const msg = agentMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a1', msg, parseMessageContent(msg), s, 'live')
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      dispose()
    })
  })

  // A historical divider restores old result metadata. It must retain the live
  // turn's indicators, and its offer is one of them.
  it('keeps the live offer through a historical turn end', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      const msg = agentMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a1', msg, parseMessageContent(msg), s, 'catchingUp')
      expect(s.chatStore.getTurnRetryOffer('a1')).toEqual(OFFER)
      dispose()
    })
  })

  // Closing the agent reclaims every composed store. A revive of the same agent
  // id must not resurface the offer of a turn that the close ended.
  it('withdraws the offer when the agent closes', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ turn_retry_offer: OFFER_WIRE })), s)
      s.chatStore.forgetAgent('a1')
      expect(s.chatStore.getTurnRetryOffer('a1')).toBeUndefined()
      dispose()
    })
  })
})
