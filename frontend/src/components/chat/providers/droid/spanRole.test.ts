import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { droidNeedsRequest, droidSpanRole } from './spanRole'
import './plugin'

const provider = AgentProvider.DROID

const toolCall = {
  type: 'tool_call',
  toolUse: {
    id: 'call',
    name: 'Read',
    input: { file_path: '/repo/a.txt' },
  },
}

describe('droidSpanRole', () => {
  it('places a tool_call on the request side and a tool_result on the result side', () => {
    expect(droidSpanRole(input(toolCall, null, provider))).toBe('request')
    expect(droidSpanRole(input({ type: 'tool_result', toolUseId: 'call', content: 'output' }, null, provider))).toBe('result')
    expect(droidSpanRole(input({ type: 'message', text: 'Native answer.' }, null, provider))).toBe('other')
  })

  it('places a tool_call stored with a completion on the result side', () => {
    expect(droidSpanRole({
      ...input(toolCall, null, provider),
      completion: MessageCompletion.INTERRUPTED,
    })).toBe('result')
  })
})

describe('droidNeedsRequest', () => {
  it('is the registered hook', () => {
    expect(providerFor(provider)?.transcript.needsRequest).toBe(droidNeedsRequest)
  })

  it('answers false for a request, which draws from its own notification', () => {
    expect(droidNeedsRequest(input({
      type: 'tool_call',
      toolUse: {
        id: 'call',
        name: 'Read',
        input: { file_path: '/repo/a.txt' },
      },
    }, null, provider))).toBe(false)
  })

  it('answers true for a result, which can omit its tool name and arguments', () => {
    expect(droidNeedsRequest(input({ type: 'tool_result', toolUseId: 'call', content: 'output' }, null, provider))).toBe(true)
  })

  it('answers false for a frame outside a tool span', () => {
    expect(droidNeedsRequest(input({ type: 'message', text: 'Native answer.' }, null, provider))).toBe(false)
  })
})
