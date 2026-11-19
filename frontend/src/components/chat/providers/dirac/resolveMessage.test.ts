import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { parseAssembledMessage } from '../../assembledMessage'
import { resolveDiracMessage } from './resolveMessage'
import './plugin'

const record = {
  type: 'leapmux.dirac.tool',
  session_id: 'child',
  parent_session_id: 'parent',
  tasklet_id: 'Witten',
  role: 'result',
  declaration: {
    type: 'tool_use',
    id: 'read-1',
    name: 'read_file',
    input: {
      paths: ['a.txt'],
    },
  },
  result: { type: 'tool_result', tool_use_id: 'read-1', content: 'Actual native file contents' },
}

function parsed(value: Record<string, unknown>, session = 'child') {
  return {
    rawText: '',
    topLevel: value,
    parentObject: value,
    wrapper: null,
    agentSessionId: session,
  }
}

describe('resolveDiracMessage', () => {
  it('keeps native request context attached to its exact presentation position', () => {
    const original = {
      type: 'leapmux.dirac.message',
      session_id: 'child',
      parent_session_id: 'parent',
      tasklet_id: 'Witten',
      message: {
        id: 'native-partial',
        conversationHistoryIndex: 0,
        content: { type: 'markdown', content: 'Partial native text', role: 'assistant' },
      },
      request_context: { message_id: 'native-request', conversation_history_index: -1, cancel_reason: 'user_cancelled' },
    }
    expect(parseAssembledMessage(resolveDiracMessage(parsed(original)))?.text).toBe('Partial native text')
    const rebound = {
      ...original,
      request_context: { ...original.request_context, message_id: 'another-request' },
      message: {
        ...original.message,
        content: { ...original.message.content, content: 'Foreign replacement' },
      },
    }
    expect(parseAssembledMessage(resolveDiracMessage({
      ...parsed(original),
      supplementalContent: { dirac_message: rebound },
    }))?.text).toBe('Partial native text')
    const cleared = {
      ...rebound,
      request_context: { ...original.request_context, cancel_reason: '' },
    }
    expect(parseAssembledMessage(resolveDiracMessage({
      ...parsed(original),
      supplementalContent: { dirac_message: cleared },
    }))?.text).toBe('Partial native text')
    expect(resolveDiracMessage(parsed({
      ...original,
      request_context: { ...original.request_context, conversation_history_index: 1 },
    }))).toBeUndefined()
  })

  it('retains an ended native declaration without inventing success or output', () => {
    const retained: Record<string, unknown> = { ...record, role: 'retained' }
    delete retained.result
    const call = providerToolCall(AgentProvider.DIRAC, retained, {
      spanId: 'native-span',
      spanType: 'read_file',
      agentSessionId: 'child',
      completion: MessageCompletion.FINISHED,
    })
    expect(call?.kind).toBe('read')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
    const view = resolveDiracMessage({
      ...parsed(retained),
      completion: MessageCompletion.FINISHED,
    })
    expect(view).not.toHaveProperty('rawOutput')
    expect(view?.status).toBeUndefined()
    expect(resolveDiracMessage(parsed(retained))).toBeUndefined()
  })

  const message = {
    type: 'leapmux.dirac.message',
    session_id: 'child',
    parent_session_id: 'parent',
    tasklet_id: 'Witten',
    message: {
      id: 'native-text',
      content: {
        type: 'markdown',
        content: 'Native child text',
        isReasoning: false,
        role: 'assistant',
      },
    },
  }

  it('reads native assistant text without changing the stored record', () => {
    const original = structuredClone(message)
    expect(parseAssembledMessage(resolveDiracMessage(parsed(message)))).toEqual({ kind: 'text', text: 'Native child text', completion: 'complete' })
    expect(message).toEqual(original)
  })

  it.each([
    [MessageCompletion.INTERRUPTED, 'interrupted'],
    [MessageCompletion.ERROR, 'error'],
    [MessageCompletion.FINISHED, 'finished'],
  ] as const)('preserves stored text completion %s', (completion, expected) => {
    expect(parseAssembledMessage(resolveDiracMessage({
      ...parsed(message),
      completion,
    }))?.completion).toBe(expected)
  })

  it('reads a scoped updated view and keeps its reasoning kind', () => {
    const original = {
      ...message,
      message: {
        ...message.message,
        content: { ...message.message.content, isReasoning: true },
      },
    }
    const latest = {
      ...original,
      message: {
        ...original.message,
        content: { ...original.message.content, content: 'Updated native reasoning' },
      },
    }
    expect(parseAssembledMessage(resolveDiracMessage({
      ...parsed(original),
      supplementalContent: { dirac_message: latest },
    }))).toEqual({ kind: 'reasoning', text: 'Updated native reasoning', completion: 'complete' })
  })

  it.each([
    { session_id: 'other' },
    { parent_session_id: 'other' },
    { tasklet_id: 'other' },
    {
      message: { ...message.message, id: 'other' },
    },
    {
      message: {
        ...message.message,
        content: { ...message.message.content, role: 'user' },
      },
    },
    {
      message: {
        ...message.message,
        content: { ...message.message.content, isReasoning: true },
      },
    },
  ])('refuses a supplemental view that changes native identity: %j', (patch) => {
    expect(parseAssembledMessage(resolveDiracMessage({
      ...parsed(message),
      supplementalContent: {
        dirac_message: {
          ...message,
          ...patch,
        },
      },
    }))?.text).toBe('Native child text')
  })

  it('keeps the native result visible through the actual tool extractor', () => {
    const before = structuredClone(record)
    const call = providerToolCall(AgentProvider.DIRAC, record, { spanId: 'native-span', spanType: 'read_file', agentSessionId: 'child' })
    expect(call?.kind).toBe('read')
    expect(JSON.stringify(call?.result)).toContain('Actual native file contents')
    expect(record).toEqual(before)
  })

  it('keeps result bytes out of the opening request', () => {
    const request = { ...record, role: 'request' }
    delete (request as Partial<typeof record>).result
    const view = resolveDiracMessage(parsed(request))
    expect(view?.sessionUpdate).toBe('tool_call')
    expect(view).not.toHaveProperty('rawOutput')
    expect(view?.rawInput).toEqual({
      paths: ['a.txt'],
      tool: 'read_file',
    })
  })

  it.each([
    { parent_session_id: '' },
    { tasklet_id: '' },
    { session_id: 'another-child' },
    { role: 'other' },
    { role: 'request' },
    {
      result: { ...record.result, tool_use_id: 'other' },
    },
    {
      declaration: { ...record.declaration, id: '' },
    },
    {
      declaration: { ...record.declaration, type: 'other' },
    },
  ])('refuses mixed native identities: %j', (patch) => {
    expect(resolveDiracMessage(parsed({
      ...record,
      ...patch,
    }))).toBeUndefined()
  })

  it('refuses an absent message session', () => {
    expect(resolveDiracMessage(parsed(record, ''))).toBeUndefined()
  })
})
