import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import { kimiRelatedMessages, kimiSpanRole } from './spanRole'

function resolved(parent: Record<string, unknown>, completion?: MessageCompletion) {
  return resolveMessageForRendering({ ...input(parent, undefined, AgentProvider.KIMI_CODE), ...(completion !== undefined ? { completion } : {}) }, AgentProvider.KIMI_CODE)
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

describe('kimiRelatedMessages', () => {
  it('pairs a result with its request', () => {
    expect(kimiRelatedMessages(resolved(kimiToolResult('c', 'x')))).toStrictEqual(['request'])
  })

  it('pairs a launch and an empty call with their result', () => {
    expect(kimiRelatedMessages(resolved(kimiToolStart('c', KIMI_TOOL.Agent, { prompt: 'p', description: 'd' })))).toStrictEqual(['result'])
    expect(kimiRelatedMessages(resolved(kimiToolStart('c', KIMI_TOOL.AgentSwarm, { description: 'd' })))).toStrictEqual(['result'])
    expect(kimiRelatedMessages(resolved(kimiToolStart('c', KIMI_TOOL.TaskList, {})))).toStrictEqual(['result'])
  })

  it('reads a start whose arguments are absent or not an object as an empty call', () => {
    const { args: _args, ...noArgs } = kimiToolStart('c', KIMI_TOOL.TaskList, {})
    expect(kimiRelatedMessages(resolved(noArgs))).toStrictEqual(['result'])
    expect(kimiRelatedMessages(resolved({ ...kimiToolStart('c', KIMI_TOOL.Read, {}), args: 'a.go' }))).toStrictEqual(['result'])
  })

  // A retained start closes its call, so it needs the request the way a result does.
  it('pairs a retained start with its request', () => {
    expect(kimiRelatedMessages(resolved(kimiToolStart('c', KIMI_TOOL.Agent, { prompt: 'p' }), MessageCompletion.INTERRUPTED))).toStrictEqual(['request'])
  })

  it('needs nothing beside a request that states its own arguments', () => {
    expect(kimiRelatedMessages(resolved(kimiToolStart('c', KIMI_TOOL.Read, { path: 'a' })))).toStrictEqual([])
    expect(kimiRelatedMessages(resolved(kimiFrame(KIMI_EVENT.TurnEnded)))).toStrictEqual([])
  })
})
