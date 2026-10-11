import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_EVENT, CODEWHALE_ITEM_KIND, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { CALL, childBlock, codewhaleEvent, itemFinished, requestSide, toolCompleted, toolFailed, toolFinished, toolStarted } from '../toolResults.fixtures'
import { codewhaleChildBlock, codewhaleEnvelope, codewhaleFrameDrawsNothing, codewhaleFrameFailed, codewhaleItem, codewhaleItemOutcome, codewhalePairedFrame, codewhaleToolFrame, codewhaleToolSpanRole } from './toolCommon'

/** A final tool event whose item states exactly this metadata, with no fixture defaults. */
function toolEnd(event: string, metadata: Record<string, unknown>, detail = 'x'): Record<string, unknown> {
  return codewhaleEvent(event, { item: { detail, metadata: { tool_use_id: CALL, tool_name: CODEWHALE_TOOL.Bash, ...metadata } } })
}

describe('codewhaleEnvelope', () => {
  it('reads the event, the turn and the payload', () => {
    expect(codewhaleEnvelope(codewhaleEvent(CODEWHALE_EVENT.TurnCompleted, { turn: {} }))).toStrictEqual({ event: CODEWHALE_EVENT.TurnCompleted, turnId: 'turn-1', payload: { turn: {} } })
  })

  it('states an empty payload for an event that sends none', () => {
    expect(codewhaleEnvelope({ event: 'x' })?.payload).toStrictEqual({})
  })

  it('states an empty payload and an empty turn for fields in another shape', () => {
    expect(codewhaleEnvelope({ event: 'x', turn_id: 7, payload: 'text' })).toStrictEqual({ event: 'x', turnId: '', payload: {} })
    expect(codewhaleEnvelope({ event: 'x', payload: [1, 2] })?.payload).toStrictEqual({})
  })

  it('answers null for a row that is not an event', () => {
    expect(codewhaleEnvelope({ content: 'hi' })).toBeNull()
    expect(codewhaleEnvelope({ event: '' })).toBeNull()
    expect(codewhaleEnvelope('text')).toBeNull()
  })
})

describe('codewhaleItemOutcome', () => {
  it('reads every item event', () => {
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.ItemStarted)).toBe('open')
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.ItemCompleted)).toBe('completed')
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.ItemFailed)).toBe('failed')
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.ItemInterrupted)).toBe('interrupted')
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.ItemCanceled)).toBe('interrupted')
    expect(codewhaleItemOutcome('item.delta')).toBeNull()
    expect(codewhaleItemOutcome(CODEWHALE_EVENT.TurnCompleted)).toBeNull()
  })
})

describe('codewhaleItem', () => {
  it('reads the item of an item event', () => {
    expect(codewhaleItem(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello.'))).toStrictEqual({ outcome: 'completed', kind: CODEWHALE_ITEM_KIND.AgentMessage, summary: 'Hello.', detail: 'Hello.', metadata: {} })
  })

  it('answers null for another event, or an item event that carries no item', () => {
    expect(codewhaleItem(codewhaleEvent(CODEWHALE_EVENT.TurnCompleted, {}))).toBeNull()
    expect(codewhaleItem(codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, {}))).toBeNull()
  })
})

describe('codewhaleToolFrame', () => {
  it('prefers the parsed arguments of a start event', () => {
    const frame = codewhaleToolFrame(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }))
    expect(frame).toStrictEqual({ callId: CALL, toolName: CODEWHALE_TOOL.Bash, input: { command: 'ls' }, outcome: 'open', text: '', metadata: { tool_use_id: CALL, tool_name: CODEWHALE_TOOL.Bash, tool_input: '{"command":"ls"}' }, deferredLoad: false })
  })

  it('parses the argument string of a final event', () => {
    expect(codewhaleToolFrame(toolCompleted(CODEWHALE_TOOL.Bash, { command: 'ls' }, 'out'))?.input).toStrictEqual({ command: 'ls' })
  })

  // A start carries the arguments twice, and the two copies agree on the wire. This
  // test makes them differ, so it can tell which copy the reader took.
  it('takes the parsed arguments over the argument string when the two differ', () => {
    const start = codewhaleEvent(CODEWHALE_EVENT.ItemStarted, {
      item: { detail: '', metadata: { tool_use_id: CALL, tool_name: CODEWHALE_TOOL.Bash, tool_input: '{"command":"from the string"}' } },
      tool: { id: CALL, name: CODEWHALE_TOOL.Bash, input: { command: 'parsed' } },
    })
    expect(codewhaleToolFrame(start)?.input).toStrictEqual({ command: 'parsed' })
  })

  it('takes the tool record\'s name and id over the metadata, and tool_use_id over tool_call_id', () => {
    const start = codewhaleEvent(CODEWHALE_EVENT.ItemStarted, {
      item: { detail: '', metadata: { tool_use_id: 'from-metadata', tool_call_id: 'from-question', tool_name: CODEWHALE_TOOL.Bash } },
      tool: { id: 'from-tool', name: CODEWHALE_TOOL.Read, input: {} },
    })
    expect(codewhaleToolFrame(start)).toMatchObject({ callId: 'from-tool', toolName: CODEWHALE_TOOL.Read })
    const end = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { detail: 'x', metadata: { tool_use_id: 'from-metadata', tool_call_id: 'from-question', tool_name: CODEWHALE_TOOL.Bash } } })
    expect(codewhaleToolFrame(end)?.callId).toBe('from-metadata')
  })

  it('answers null for an item that names a call but no tool', () => {
    expect(codewhaleToolFrame(codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { detail: 'x', metadata: { tool_use_id: CALL } } }))).toBeNull()
  })

  it('states no arguments for a string that does not parse into an object', () => {
    const broken = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { detail: '', metadata: { tool_use_id: CALL, tool_name: 'x', tool_input: '{nope' } } })
    const array = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { detail: '', metadata: { tool_use_id: CALL, tool_name: 'x', tool_input: '[1]' } } })
    expect(codewhaleToolFrame(broken)?.input).toStrictEqual({})
    expect(codewhaleToolFrame(array)?.input).toStrictEqual({})
  })

  it('reads the redacted question result by its tool_call_id', () => {
    const answered = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { detail: 'User input submitted', metadata: { tool_call_id: 'q1', tool_name: CODEWHALE_TOOL.RequestUserInput, response_redacted: true } } })
    expect(codewhaleToolFrame(answered)).toMatchObject({ callId: 'q1', outcome: 'completed', text: 'User input submitted' })
  })

  it('marks the first call of a deferred tool', () => {
    expect(codewhaleToolFrame(toolCompleted(CODEWHALE_TOOL.ApplyPatch, {}, 'loaded', { deferred_tool_loaded: true }))?.deferredLoad).toBe(true)
  })

  // A subagent's transcript states a result as text alone, so the runtime's own
  // opening sentence is what marks the schema load there.
  it('marks the first call of a deferred tool in a subagent transcript by its result sentence', () => {
    const loaded = 'Tool `apply_patch` was deferred and has now been loaded. Retry the call with the newly available schema.'
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: loaded }))?.deferredLoad).toBe(true)
    // The sentence opens the result, or it marks nothing: a result that quotes it
    // is a call that ran.
    const quoted = `The log says: ${loaded}`
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: quoted }))?.deferredLoad).toBe(false)
  })

  it('reads the two tool blocks of a subagent transcript', () => {
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: 'b1', name: 'read', input: { path: 'a' } })))
      .toStrictEqual({ callId: 'b1', toolName: 'read', input: { path: 'a' }, outcome: 'open', text: '', metadata: {}, deferredLoad: false })
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x', is_error: true })))
      .toMatchObject({ callId: 'b1', toolName: '', outcome: 'failed', text: 'x' })
  })

  it('reads a result block with no text of its own as an empty answer that ran', () => {
    for (const content of [undefined, 42, { text: 'not a list' }]) {
      const block = content === undefined ? { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1' } : { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content }
      expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, block)), String(content)).toMatchObject({ callId: 'b1', outcome: 'completed', text: '', deferredLoad: false })
    }
  })

  // Only a boolean `true` marks an error, as it does on the item metadata.
  it('reads a result block as failed only for an is_error of true', () => {
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x', is_error: 'true' }))?.outcome).toBe('completed')
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'x', is_error: false }))?.outcome).toBe('completed')
  })

  // The classifier, the span role and the extractor read one row in turn, and each
  // reads the frame the first one built.
  it('builds one frame for each row and answers the same frame again', () => {
    const row = toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' })
    const first = codewhaleToolFrame(row)
    expect(first).not.toBeNull()
    expect(codewhaleToolFrame(row)).toBe(first)
    // A copy of the row is another row, and it takes a frame of its own.
    expect(codewhaleToolFrame({ ...row })).not.toBe(first)
    expect(codewhaleToolFrame({ ...row })).toStrictEqual(first)
    const message = itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'x')
    expect(codewhaleToolFrame(message)).toBeNull()
    expect(codewhaleToolFrame(message)).toBeNull()
  })

  it('answers null for a row that states no call', () => {
    expect(codewhaleToolFrame(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'x'))).toBeNull()
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, name: 'read' }))).toBeNull()
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: 'b1' }))).toBeNull()
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult }))).toBeNull()
    expect(codewhaleToolFrame(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'x' }))).toBeNull()
    expect(codewhaleToolFrame(null)).toBeNull()
  })
})

describe('codewhaleChildBlock', () => {
  it('reads the one block a stored row carries', () => {
    expect(codewhaleChildBlock(childBlock('assistant', { type: 'text', text: 'x' }))).toStrictEqual({ role: 'assistant', type: 'text', block: { type: 'text', text: 'x' } })
  })

  it('answers null for a header, a row without an index, and a row of several blocks', () => {
    expect(codewhaleChildBlock({ kind: 'subagent_transcript_header', agent_id: 'a1' })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', message: { role: 'assistant', content: [{ type: 'text' }] } })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', index: 1, message: { role: 'assistant', content: [{ type: 'text' }, { type: 'text' }] } })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', index: 1, message: { role: 'assistant', content: [{ text: 'no type' }] } })).toBeNull()
  })

  it('answers null for a row whose content holds no block record', () => {
    expect(codewhaleChildBlock({ kind: 'message', index: 1, message: { role: 'assistant', content: [] } })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', index: 1, message: { role: 'assistant', content: ['text'] } })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', index: 1, message: { role: 'assistant', content: 'text' } })).toBeNull()
    expect(codewhaleChildBlock({ kind: 'message', index: 1 })).toBeNull()
    // An index that is not a number is a row this reader cannot place.
    expect(codewhaleChildBlock({ kind: 'message', index: '1', message: { role: 'assistant', content: [{ type: 'text' }] } })).toBeNull()
  })

  it('states an empty role for a message that sends none', () => {
    expect(codewhaleChildBlock({ kind: 'message', index: 0, message: { content: [{ type: 'text', text: 'x' }] } })).toStrictEqual({ role: '', type: 'text', block: { type: 'text', text: 'x' } })
  })
})

describe('codewhaleFrameDrawsNothing', () => {
  const frame = (row: Record<string, unknown>) => codewhaleToolFrame(row)!

  it('hides the result of a deferred tool\'s first call, however it ended', () => {
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemCompleted, { deferred_tool_loaded: true })))).toBe(true)
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemFailed, { deferred_tool_loaded: true })))).toBe(true)
  })

  it('hides the completed result of a question whose answers the runtime redacted', () => {
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemCompleted, { response_redacted: true })))).toBe(true)
  })

  // A question that failed or that the turn cut short states why, and that is what
  // the reader needs to see.
  it('keeps the redacted result of a question that did not complete', () => {
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemFailed, { response_redacted: true })))).toBe(false)
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemInterrupted, { response_redacted: true })))).toBe(false)
  })

  // The opening frame is the only row that states the call's arguments, so no
  // metadata flag hides it.
  it('never hides an opening frame', () => {
    const start = (metadata: Record<string, unknown>) => frame(codewhaleEvent(CODEWHALE_EVENT.ItemStarted, { item: { detail: '', metadata: { tool_use_id: CALL, tool_name: CODEWHALE_TOOL.Bash, ...metadata } } }))
    expect(codewhaleFrameDrawsNothing(start({ deferred_tool_loaded: true }))).toBe(false)
    expect(codewhaleFrameDrawsNothing(start({ response_redacted: true }))).toBe(false)
  })

  it('keeps an ordinary result, and reads only a boolean true as a flag', () => {
    expect(codewhaleFrameDrawsNothing(frame(toolCompleted(CODEWHALE_TOOL.Bash, {}, 'x')))).toBe(false)
    expect(codewhaleFrameDrawsNothing(frame(toolEnd(CODEWHALE_EVENT.ItemCompleted, { deferred_tool_loaded: 'true', response_redacted: 1 })))).toBe(false)
  })
})

describe('codewhaleFrameFailed', () => {
  it('reads a failed event and a completed call that reports an error', () => {
    const failed = codewhaleToolFrame(codewhaleEvent(CODEWHALE_EVENT.ItemFailed, { item: { detail: 'x', metadata: { tool_use_id: CALL, tool_name: 'bash' } } }))!
    expect(codewhaleFrameFailed(failed)).toBe(true)
    expect(codewhaleFrameFailed(codewhaleToolFrame(toolCompleted('bash', {}, 'x', { is_error: true }))!)).toBe(true)
    expect(codewhaleFrameFailed(codewhaleToolFrame(toolCompleted('bash', {}, 'x'))!)).toBe(false)
  })

  // A stopped call is its own outcome: the row states it as cancelled, not failed.
  it('reads neither a stopped call nor an open one as failed', () => {
    expect(codewhaleFrameFailed(codewhaleToolFrame(toolFinished(CODEWHALE_EVENT.ItemInterrupted, 'bash', {}, 'Killed'))!)).toBe(false)
    expect(codewhaleFrameFailed(codewhaleToolFrame(toolStarted('bash', {}))!)).toBe(false)
    expect(codewhaleFrameFailed(codewhaleToolFrame(toolFailed('bash', {}, 'no'))!)).toBe(true)
  })
})

describe('codewhaleToolSpanRole', () => {
  it('reads the role from the frame', () => {
    expect(codewhaleToolSpanRole(codewhaleToolFrame(toolStarted('bash', {}))!, undefined)).toBe('request')
    expect(codewhaleToolSpanRole(codewhaleToolFrame(toolCompleted('bash', {}, 'x'))!, undefined)).toBe('result')
  })

  it('reads a retained opening frame as the result', () => {
    const frame = codewhaleToolFrame(toolStarted('bash', {}))!
    expect(codewhaleToolSpanRole(frame, { ...requestSide(toolStarted('bash', {})), completion: MessageCompletion.ERROR })).toBe('result')
  })

  it('reads an opening frame that carries a tool-outcome note as the result', () => {
    const frame = codewhaleToolFrame(toolStarted('bash', {}))!
    const noted = { ...requestSide(toolStarted('bash', {})), messageMetadata: { tool_outcome: { source: 'batch_summary', outcome: 'succeeded' } } }
    expect(codewhaleToolSpanRole(frame, noted)).toBe('result')
    // A note that states no source is no note, and the frame decides again.
    const partial = { ...requestSide(toolStarted('bash', {})), messageMetadata: { tool_outcome: { outcome: 'succeeded' } } }
    expect(codewhaleToolSpanRole(frame, partial)).toBe('request')
  })
})

describe('codewhalePairedFrame', () => {
  it('accepts the paired frame of the same call alone', () => {
    const own = codewhaleToolFrame(toolCompleted('bash', {}, 'x'))!
    expect(codewhalePairedFrame(own, requestSide(toolStarted('bash', { command: 'ls' })))?.input).toStrictEqual({ command: 'ls' })
    expect(codewhalePairedFrame(own, requestSide(toolStarted('bash', {}, 'call-2')))).toBeNull()
    expect(codewhalePairedFrame(own, undefined)).toBeNull()
  })
})
