import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { diracAttachmentRequest } from './attachmentInput'

function request(operation: 'complete' | 'progress' = 'complete', feedback = '<feedback>\n\n</feedback>', tool = 'respond') {
  const call = diracRespondToolCall('answer', operation, 'The prior turn ended.')
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: {
      messages: [
        {
          role: 'assistant',
          tool_calls: [
            {
              id: call.id,
              type: 'function',
              function: {
                name: tool,
                arguments: JSON.stringify(call.arguments),
              },
            },
          ],
        },
        { role: 'tool', tool_call_id: call.id, content: 'The prior turn ended.' },
        {
          role: 'user',
          content: [
            { type: 'text', text: feedback },
            {
              type: 'image_url',
              image_url: { url: 'data:image/png;base64,ACTUAL_IMAGE_BYTES' },
            },
          ],
        },
      ],
    },
  } satisfies MockModelRequestRecord
}

describe('diracAttachmentRequest', () => {
  it.each([1, 2])('keeps an attachment-only native user row with %i images after a completed response', (count) => {
    const input = request()
    const row = input.body.messages[2]!
    row.content = Array.from({ length: count }, (_, index) => ({
      type: 'image_url',
      image_url: { url: `data:image/png;base64,ORIGINAL_BYTES_${index}` },
    }))
    const original = structuredClone(input)
    const result = diracAttachmentRequest(input)
    expect(result.body).toEqual({
      messages: [original.body.messages[2]],
    })
    expect((result.body as { messages: unknown[] }).messages[0]).toBe(row)
    expect(input).toEqual(original)
    expect(result.path).toBe(input.path)
  })

  it.each([
    { content: [] },
    {
      content: [
        { type: 'image_url' },
      ],
    },
    {
      content: [
        {
          type: 'image_url',
          image_url: { url: '' },
        },
      ],
    },
    {
      content: [
        {
          type: 'image_url',
          image_url: { url: ' ' },
        },
      ],
    },
    {
      content: [
        { type: 'audio', data: 'not a native image' },
      ],
    },
  ])('rejects a malformed attachment-only user row: $content', ({ content }) => {
    const input = request()
    const row = { role: 'user', content }
    const altered = {
      ...input,
      body: {
        messages: [
          ...input.body.messages.slice(0, 2),
          row,
        ],
      },
    }
    expect(() => diracAttachmentRequest(altered)).toThrow('native user feedback envelope')
  })

  it('keeps the actual next user row and its image without changing the native receipt', () => {
    const input = request()
    const original = structuredClone(input)
    const result = diracAttachmentRequest(input)
    expect(result.body).toEqual({
      messages: [original.body.messages[2]],
    })
    expect(input).toEqual(original)
    expect(result.path).toBe(input.path)
  })

  it('keeps a first user turn unchanged', () => {
    const input: MockModelRequestRecord = {
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      body: {
        messages: [
          {
            role: 'user',
            content: [
              {
                type: 'image_url',
                image_url: { url: 'data:image/png;base64,IMAGE' },
              },
            ],
          },
        ],
      },
    }
    expect(diracAttachmentRequest(input)).toBe(input)
  })

  it('rejects a nonfinal native tool before a synthetic user image', () => {
    expect(() => diracAttachmentRequest(request('progress'))).toThrow('did not complete the prior turn')
  })

  it('rejects another tool even when its arguments claim completion', () => {
    expect(() => diracAttachmentRequest(request('complete', '<feedback>\n\n</feedback>', 'read_file'))).toThrow('did not complete the prior turn')
  })

  it.each([
    '',
    '<feedback>wrong',
    'tool image output',
    '<task>\ntext\n</task>',
  ])('rejects a missing native feedback envelope: %j', (feedback) => {
    expect(() => diracAttachmentRequest(request('complete', feedback))).toThrow('native user feedback envelope')
  })

  it.each([
    {},
    { messages: null },
    { messages: 'invalid' },
  ])('rejects an invalid native request: %j', (body) => {
    expect(() => diracAttachmentRequest({ protocol: 'openai-chat-completions', path: '/', body })).toThrow('native message array')
  })
})
