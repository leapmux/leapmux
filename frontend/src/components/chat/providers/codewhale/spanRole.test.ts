import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { codewhaleRelatedMessages, codewhaleSpanRole } from './spanRole'
import { childBlock, itemFinished, requestSide, toolCompleted, toolFailed, toolStarted } from './toolResults.fixtures'

const ARGS = { path: 'a.ts' }

describe('codewhaleSpanRole', () => {
  it('opens a call on an item start and on a tool_use block', () => {
    expect(codewhaleSpanRole(requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)))).toBe('request')
    expect(codewhaleSpanRole(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: 'b1', name: CODEWHALE_TOOL.Read, input: ARGS })))).toBe('request')
  })

  it('ends a call on a final item event and on a tool_result block', () => {
    expect(codewhaleSpanRole(requestSide(toolCompleted(CODEWHALE_TOOL.Read, ARGS, 'x')))).toBe('result')
    expect(codewhaleSpanRole(requestSide(toolFailed(CODEWHALE_TOOL.Read, ARGS, 'no')))).toBe('result')
    expect(codewhaleSpanRole(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x' })))).toBe('result')
  })

  it('reads a retained start as final', () => {
    const retained = { ...requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)), completion: MessageCompletion.INTERRUPTED }
    expect(codewhaleSpanRole(retained)).toBe('result')
  })

  it('states no role for a row that is not a tool call', () => {
    expect(codewhaleSpanRole(requestSide(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.')))).toBe('other')
    expect(codewhaleSpanRole(requestSide({ content: 'Hi' }))).toBe('other')
  })
})

describe('codewhaleRelatedMessages', () => {
  // A result block states neither the tool nor its arguments, so it needs its
  // request. A request needs its result, which alone says whether the call was a
  // deferred tool's schema load that draws nothing.
  it('pairs each side of a call with the other', () => {
    expect(codewhaleRelatedMessages(requestSide(toolCompleted(CODEWHALE_TOOL.Read, ARGS, 'x')))).toStrictEqual(['request'])
    expect(codewhaleRelatedMessages(requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)))).toStrictEqual(['result'])
  })

  it('links nothing for a row that is not a tool call', () => {
    expect(codewhaleRelatedMessages(requestSide(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.')))).toStrictEqual([])
  })

  // A retained opening frame is the END of its span, so it asks for the request
  // that opened the span, never for a result that the turn cut off.
  it('pairs a retained opening frame with its request', () => {
    const retained = { ...requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)), completion: MessageCompletion.INTERRUPTED }
    expect(codewhaleRelatedMessages(retained)).toStrictEqual(['request'])
  })
})
