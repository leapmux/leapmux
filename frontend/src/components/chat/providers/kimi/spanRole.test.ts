import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiTranscriptToolFrame, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
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
  it('reads native tool state and retained completion', () => {
    const frame = {
      name: 'Bash',
      input: { command: 'printf ok' },
    }
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame(frame)))).toBe('request')
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({ ...frame, state: 'done', output: 'ok' })))).toBe('result')
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame(frame), MessageCompletion.INTERRUPTED))).toBe('result')
  })

  it('opens a span with a start and closes it with a result', () => {
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Read,
      input: { path: 'a' },
    })))).toBe('request')
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Read,
      input: { path: 'a' },
      state: 'done',
      output: 'x',
    })))).toBe('result')
  })

  it('closes the span with a retained start, whichever way the turn ended', () => {
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR]) {
      expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({
        toolCallId: 'c',
        name: KIMI_TOOL.Bash,
        input: { command: 'x' },
      }), completion)), String(completion)).toBe('result')
    }
    expect(kimiSpanRole(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Bash,
      input: { command: 'x' },
    }), MessageCompletion.UNSPECIFIED))).toBe('request')
  })

  it('reads every other row as outside a span', () => {
    expect(kimiSpanRole(resolved(kimiTranscriptTurnFrame({ state: 'completed' })))).toBe('other')
    expect(kimiSpanRole(resolved({ content: 'hi' }))).toBe('other')
  })
})

describe('kimiNeedsRequest', () => {
  it('pairs a result with its request', () => {
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Read,
      input: { path: 'a' },
      state: 'done',
      output: 'x',
    })))).toBe(true)
  })

  it('never requests later rows for a launch or an empty call', () => {
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Agent,
      input: { prompt: 'p', description: 'd' },
    })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.AgentSwarm,
      input: { description: 'd' },
    })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({ toolCallId: 'c', name: KIMI_TOOL.TaskList, input: {} })))).toBe(false)
  })

  it('reads a start whose arguments are absent or not an object as an empty call', () => {
    for (const input of [undefined, 'a.go', null]) {
      expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({ toolCallId: 'c', name: KIMI_TOOL.Read, input })))).toBe(false)
    }
  })

  // A retained start closes its call, so it needs the request the way a result does.
  it('pairs a retained start with its request', () => {
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Agent,
      input: { prompt: 'p' },
    }), MessageCompletion.INTERRUPTED))).toBe(true)
  })

  it('needs nothing beside a request that states its own arguments', () => {
    expect(kimiNeedsRequest(resolved(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Read,
      input: { path: 'a' },
    })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiTranscriptTurnFrame({ state: 'completed' })))).toBe(false)
  })
})
