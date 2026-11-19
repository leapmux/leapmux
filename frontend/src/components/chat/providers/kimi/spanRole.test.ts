import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiConversationToolRecord, kimiConversationTurnRecord, kimiTranscriptToolFrame } from '~/test-support/kimiFixtures'
import { resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import { kimiNeedsRequest, kimiSpanRole } from './spanRole'

function resolved(parent: Record<string, unknown>, completion?: MessageCompletion) {
  return resolveMessageForRendering({
    ...input(parent, undefined, AgentProvider.KIMI_CODE),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }, AgentProvider.KIMI_CODE)
}

describe('kimiSpanRole', () => {
  const call = {
    item_id: 'shell',
    name: KIMI_TOOL.Bash,
    input: { command: 'printf ok' },
  }

  it('opens only a native request and closes only a native result', () => {
    expect(kimiSpanRole(resolved(kimiConversationToolRecord('request', call)))).toBe('request')
    expect(kimiSpanRole(resolved(kimiConversationToolRecord('result', {
      ...call,
      status: 'ended',
      outcome: 'completed',
      output: 'ok',
    })))).toBe('result')
  })

  it.each([
    MessageCompletion.UNSPECIFIED,
    MessageCompletion.COMPLETE,
    MessageCompletion.INTERRUPTED,
    MessageCompletion.ERROR,
  ])('keeps the explicit role for worker completion %s', (completion) => {
    expect(kimiSpanRole(resolved(kimiConversationToolRecord('request', call), completion))).toBe('request')
    expect(kimiSpanRole(resolved(kimiConversationToolRecord('result', call), completion))).toBe('result')
  })

  it('keeps other native rows and obsolete tool envelopes outside a span', () => {
    expect(kimiSpanRole(resolved(kimiConversationTurnRecord({ status: 'ended', outcome: 'completed' })))).toBe('other')
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({ name: KIMI_TOOL.Bash, input: call.input }), MessageCompletion.INTERRUPTED))).toBe('other')
    expect(kimiSpanRole(resolved({ content: 'hi' }))).toBe('other')
  })
})

describe('kimiNeedsRequest', () => {
  it('reads the earlier side for a completed result and a retained running item', () => {
    const read = {
      item_id: 'read',
      name: KIMI_TOOL.Read,
      input: { path: 'a' },
    }
    expect(kimiNeedsRequest(resolved(kimiConversationToolRecord('result', {
      ...read,
      status: 'ended',
      outcome: 'completed',
      output: 'x',
    })))).toBe(true)
    expect(kimiNeedsRequest(resolved(kimiConversationToolRecord('result', read), MessageCompletion.INTERRUPTED))).toBe(true)
  })

  it.each([
    KIMI_TOOL.Agent,
    KIMI_TOOL.AgentSwarm,
    KIMI_TOOL.TaskList,
    KIMI_TOOL.Read,
  ])('reads no later rows for a %s request', (name) => {
    expect(kimiNeedsRequest(resolved(kimiConversationToolRecord('request', { name, input: {} })))).toBe(false)
  })

  it.each([undefined, 'a.go', null])('reads no later row when request arguments are %j', (toolInput) => {
    expect(kimiNeedsRequest(resolved(kimiConversationToolRecord('request', { name: KIMI_TOOL.Read, input: toolInput })))).toBe(false)
  })

  it('reads no earlier request for a native turn', () => {
    expect(kimiNeedsRequest(resolved(kimiConversationTurnRecord({ status: 'ended', outcome: 'completed' })))).toBe(false)
  })
})
