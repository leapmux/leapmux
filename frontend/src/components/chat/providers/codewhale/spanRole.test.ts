import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { codewhaleNeedsRequest, codewhaleSpanRole } from './spanRole'
import { childBlock, itemFinished, requestSide, toolCompleted, toolFailed, toolStarted } from './toolResults.fixtures'

const ARGS = { path: 'a.ts' }

describe('codewhaleSpanRole', () => {
  it('opens a call on an item start and on a tool_use block', () => {
    expect(codewhaleSpanRole(requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)))).toBe('request')
    expect(codewhaleSpanRole(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, {
      type: CODEWHALE_BLOCK_TYPE.ToolUse,
      id: 'b1',
      name: CODEWHALE_TOOL.Read,
      input: ARGS,
    })))).toBe('request')
  })

  it('ends a call on a final item event and on a tool_result block', () => {
    expect(codewhaleSpanRole(requestSide(toolCompleted(CODEWHALE_TOOL.Read, ARGS, 'x')))).toBe('result')
    expect(codewhaleSpanRole(requestSide(toolFailed(CODEWHALE_TOOL.Read, ARGS, 'no')))).toBe('result')
    expect(codewhaleSpanRole(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x' })))).toBe('result')
  })

  it('reads a retained start as final', () => {
    const retained = {
      ...requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(codewhaleSpanRole(retained)).toBe('result')
  })

  it('states no role for a row that is not a tool call', () => {
    expect(codewhaleSpanRole(requestSide(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.')))).toBe('other')
    expect(codewhaleSpanRole(requestSide({ content: 'Hi' }))).toBe('other')
  })
})

// A result block states neither the tool nor its arguments, so its row reads them from
// the `tool_use` block. A request never waits for its result.
describe('codewhaleNeedsRequest', () => {
  it('answers false for an opening frame', () => {
    expect(codewhaleNeedsRequest(requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)))).toBe(false)
    expect(codewhaleNeedsRequest(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, {
      type: CODEWHALE_BLOCK_TYPE.ToolUse,
      id: 'b1',
      name: CODEWHALE_TOOL.Read,
      input: ARGS,
    })))).toBe(false)
  })

  it('answers true for a result block, which states no tool and no arguments', () => {
    expect(codewhaleNeedsRequest(requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x' })))).toBe(true)
  })

  // A final item states its tool, and it repeats the arguments as a JSON string.
  it('answers by whether a final item repeats its arguments', () => {
    expect(codewhaleNeedsRequest(requestSide(toolCompleted(CODEWHALE_TOOL.Read, ARGS, 'x')))).toBe(false)
    expect(codewhaleNeedsRequest(requestSide(toolCompleted(CODEWHALE_TOOL.Read, {}, 'x')))).toBe(true)
  })

  it('answers false for a row that is not a tool call', () => {
    expect(codewhaleNeedsRequest(requestSide(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.')))).toBe(false)
  })

  // A retained opening frame is the END of its span, but it states its own tool and
  // arguments.
  it('answers false for a retained opening frame', () => {
    const retained = {
      ...requestSide(toolStarted(CODEWHALE_TOOL.Read, ARGS)),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(codewhaleNeedsRequest(retained)).toBe(false)
  })

  // Its row reads its own frame as the request, also for a tool that takes no argument,
  // so the row never waits for a request that it would not read.
  it('answers false for a retained opening frame of a tool that takes no argument', () => {
    const retained = {
      ...requestSide(toolStarted(CODEWHALE_TOOL.Read, {})),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(codewhaleNeedsRequest(retained)).toBe(false)
  })
})
