import { describe, expect, it } from 'vitest'
import { MESSAGE_SUPPLEMENT_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, ContentCompression, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { buildRawJsonEnvelope } from '../../chatRawJson'
import { input } from '../testUtils'
import { classifyACPMessage } from './classification'
import { acpToolPresentation, resolveACPMessage } from './toolPresentation'

describe('file change confirmation (ACP)', () => {
  it.each(['pending', 'completed', 'failed', 'cancelled'])('keeps requested changes separate from a %s result', (status) => {
    const presentation = acpToolPresentation({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status,
      rawInput: { filePath: '/project/example.ts', oldString: 'before', newString: 'after' },
      content: [{ type: 'content', content: { type: 'text', text: 'No file changes occurred.' } }],
    })
    expect(presentation.body.type).toBe('text')
    expect(presentation.output).toBe('No file changes occurred.')
    expect(presentation.requestedChanges).toEqual([expect.objectContaining({ filePath: '/project/example.ts', oldStr: 'before', newStr: 'after' })])
  })

  it('uses confirmed provider differences instead of a requested difference', () => {
    const presentation = acpToolPresentation({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { filePath: '/project/example.ts', oldString: 'before', newString: 'requested' },
      content: [{ type: 'diff', path: '/project/example.ts', oldText: 'before', newText: 'confirmed' }],
    })
    expect(presentation.body).toMatchObject({ type: 'diff', sources: [{ newStr: 'confirmed' }] })
    expect(presentation.requestedChanges).toBeUndefined()
  })
})

describe('result wrapper resolution (ACP)', () => {
  it('resolves native result fields while retaining the original wrapper', () => {
    const raw = ' {"id":"native-result","role":"result","future":9007199254740993,"content":{"stopReason":"end_turn","usage":{"totalTokens":0}}} '
    const message = makeMessage({ agentProvider: AgentProvider.OPENCODE, content: new TextEncoder().encode(raw), contentCompression: ContentCompression.NONE })
    const parsed = parseMessageContent(message)
    const resolved = resolveACPMessage(parsed)
    expect(resolved).toEqual({ stopReason: 'end_turn', usage: { totalTokens: 0 } })
    expect(classifyACPMessage()({ ...parsed, parentObject: resolved }).kind).toBe('result_divider')
    expect(parsed.parentObject?.role).toBe('result')
    expect(parsed.rawText).toBe(raw)
    expect(buildRawJsonEnvelope(message, parsed, 'agent')).toContain(`"content":${raw}`)
  })

  it.each([
    { role: 'assistant', content: { text: 'Keep the message' } },
    { stopReason: 'end_turn' },
    { role: 'result', content: null },
    { role: 'result', content: 0 },
    { role: 'result', content: [] },
  ])('retains an unwrapped or invalid result shape (%j)', (original) => {
    expect(resolveACPMessage(input(original))).toEqual(original)
  })
})

describe('an interrupted tool call (ACP)', () => {
  // The worker stores the LAST frame the agent sent, byte for byte, and keeps every
  // field an earlier frame carried in the supplement. The row therefore holds the
  // agent's own in_progress status, and the interruption lives in the completion
  // column alone.
  const original = {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'tc-1',
    status: 'in_progress',
    content: [{ type: 'content', content: { type: 'text', text: 'partial output' } }],
  }
  const message = makeMessage({
    agentProvider: AgentProvider.OPENCODE,
    completion: MessageCompletion.INTERRUPTED,
    content: rawContent(original),
    supplementalContent: rawContent({
      [MESSAGE_SUPPLEMENT_FIELD.Provider]: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'tc-1',
        status: 'in_progress',
        protocol: { title: 'printf partial', kind: 'execute' },
      },
    }),
  })

  it('recovers the opening frame fields from the supplement', () => {
    expect(resolveACPMessage(parseMessageContent(message))).toEqual({
      ...original,
      title: 'printf partial',
      kind: 'execute',
    })
  })

  it('presents the recovered command and the partial output', () => {
    const parsed = parseMessageContent(message)
    const presentation = acpToolPresentation(
      resolveACPMessage(parsed)!,
      undefined,
      parsed.supplementalContent,
      message.completion,
    )
    expect(presentation.kind).toBe('execute')
    expect(presentation.title).toBe('printf partial')
    expect(presentation.output).toBe('partial output')
  })

  it('classifies the row as a tool use although its status is not final', () => {
    const parsed = parseMessageContent(message)
    expect(classifyACPMessage()({ ...parsed, completion: message.completion }).kind).toBe('tool_use')
  })
})
