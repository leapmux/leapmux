import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
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
  it('opens a span with a start and closes it with a result', () => {
    expect(kimiSpanRole(resolved(kimiToolStart('c', KIMI_TOOL.Read, { path: 'a' })))).toBe('request')
    expect(kimiSpanRole(resolved(kimiToolResult('c', 'x')))).toBe('result')
  })

  it('closes the span with a retained start, whichever way the turn ended', () => {
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(kimiSpanRole(resolved(kimiToolStart('c', KIMI_TOOL.Bash, { command: 'x' }), completion)), String(completion)).toBe('result')
    expect(kimiSpanRole(resolved(kimiToolStart('c', KIMI_TOOL.Bash, { command: 'x' }), MessageCompletion.UNSPECIFIED))).toBe('request')
  })

  it('reads every other row as outside a span', () => {
    expect(kimiSpanRole(resolved(kimiFrame(KIMI_EVENT.TurnEnded)))).toBe('other')
    expect(kimiSpanRole(resolved({ content: 'hi' }))).toBe('other')
  })
})

// A result states no tool name, no arguments and no display, so its row reads them from
// the start. A start never waits for its result, also a subagent launch whose run only
// the result reports.
describe('kimiNeedsRequest', () => {
  it('answers true for a result', () => {
    expect(kimiNeedsRequest(resolved(kimiToolResult('c', 'x')))).toBe(true)
  })

  it('answers false for every start, also a launch and a call with empty arguments', () => {
    expect(kimiNeedsRequest(resolved(kimiToolStart('c', KIMI_TOOL.Read, { path: 'a' })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiToolStart('c', KIMI_TOOL.Agent, { prompt: 'p', description: 'd' })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiToolStart('c', KIMI_TOOL.AgentSwarm, { description: 'd' })))).toBe(false)
    expect(kimiNeedsRequest(resolved(kimiToolStart('c', KIMI_TOOL.TaskList, {})))).toBe(false)
  })

  // A retained start closes its call, but it states its own name and arguments.
  it('answers false for a retained start', () => {
    expect(kimiNeedsRequest(resolved(kimiToolStart('c', KIMI_TOOL.Agent, { prompt: 'p' }), MessageCompletion.INTERRUPTED))).toBe(false)
  })

  it('answers false for a row outside a span', () => {
    expect(kimiNeedsRequest(resolved(kimiFrame(KIMI_EVENT.TurnEnded)))).toBe(false)
  })
})
