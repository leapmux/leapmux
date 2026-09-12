import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import './providers/claude'
import './providers/opencode'
import './providers/cursor/plugin'
import './providers/testMocks'

const { imageFromMessage, messageToolResultImages, resolveChatImage } = await import('./chatImageResolve')

const PNG = 'iVBORw0KGgo='

function claudeImageMessage(datas: string[], seq = 7n): AgentChatMessage {
  return makeMessage({
    seq,
    spanType: 'mcp__playwright__screenshot',
    agentProvider: AgentProvider.CLAUDE_CODE,
    content: rawContent({
      type: 'user',
      message: {
        role: 'user',
        content: [{
          type: 'tool_result',
          tool_use_id: 'r1',
          content: datas.map(data => ({ type: 'image', data, mimeType: 'image/png' })),
        }],
      },
    }),
  })
}

const ref = { workerId: 'w1', agentId: 'a1', seq: 7n, imageIndex: 0 }

describe('messageToolResultImages', () => {
  it('routes through the message provider plugin, keeping wire order', () => {
    expect(messageToolResultImages(claudeImageMessage(['first', 'second'])).map(i => i.data))
      .toEqual(['first', 'second'])
  })

  it('returns an empty list for a message with no images', () => {
    expect(messageToolResultImages(makeMessage({ content: rawContent({ type: 'user' }) }))).toEqual([])
  })

  it('returns an empty list rather than throwing on unparseable content', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(messageToolResultImages(makeMessage({ content: new Uint8Array([0xFF, 0xFE]) }))).toEqual([])
    warn.mockRestore()
  })
})

describe('imageFromMessage', () => {
  it('picks the image at the index', () => {
    expect(imageFromMessage(claudeImageMessage(['first', 'second']), 1)?.data).toBe('second')
  })

  it('is null when the index is past the end', () => {
    expect(imageFromMessage(claudeImageMessage(['only']), 3)).toBeNull()
  })
})

describe('resolveChatImage', () => {
  it('uses newer supplemental images found while it loads the paired request', async () => {
    const original = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'image-call',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'image', mimeType: 'image/png', data: 'old-image' } }],
    }
    const message = makeMessage({ id: 'image-result', seq: 7n, spanId: 'image-call', agentProvider: AgentProvider.CURSOR, content: rawContent(original) })
    const updated = makeMessage({
      ...message,
      supplementalRevision: 1n,
      supplementalContent: rawContent({ provider: {
        sessionUpdate: original.sessionUpdate,
        toolCallId: original.toolCallId,
        status: original.status,
        rawOutput: { content: [{ type: 'tool-result', toolCallId: 'image-call', toolName: 'mcp_probe_image', experimental_content: [{ type: 'image', mimeType: 'image/png', data: PNG }] }] },
      } }),
    })
    const context = testMessageContext({
      fetchMessage: async () => message,
      fetchSpan: async () => [updated],
    })
    const result = await resolveChatImage(ref, context)
    expect(result).toEqual({ status: 'ready', source: { data: PNG, mimeType: 'image/png' } })
    expect(message.supplementalRevision).toBe(0n)
  })

  it('resolves from the loaded window without fetching', async () => {
    const fetchMessage = vi.fn(async () => undefined)
    const result = await resolveChatImage(ref, testMessageContext({
      messageBySeq: () => claudeImageMessage([PNG]),
      fetchMessage,
    }))
    expect(result).toEqual({ status: 'ready', source: { data: PNG, mimeType: 'image/png' } })
    expect(fetchMessage).not.toHaveBeenCalled()
  })

  it('fetches when the message is outside the loaded window', async () => {
    const fetchMessage = vi.fn(async () => claudeImageMessage([PNG]))
    const result = await resolveChatImage(ref, testMessageContext({ fetchMessage }))
    expect(result.status).toBe('ready')
    expect(fetchMessage).toHaveBeenCalledWith(7n, expect.any(AbortSignal))
  })

  it('reports `gone` for a message the worker no longer has', async () => {
    // A definitive absence: the row was deleted or the seqs moved. Retrying
    // cannot help, and the tab has to say so rather than spin.
    expect(await resolveChatImage(ref, testMessageContext())).toEqual({ status: 'gone' })
  })

  it('reports `gone` when the message exists but holds no image at that index', async () => {
    const result = await resolveChatImage({ ...ref, imageIndex: 4 }, testMessageContext({
      messageBySeq: () => claudeImageMessage([PNG]),
    }))
    expect(result).toEqual({ status: 'gone' })
  })

  it('reports a retryable error when the fetch itself fails', async () => {
    const result = await resolveChatImage(ref, testMessageContext({
      fetchMessage: async () => {
        throw new Error('channel closed')
      },
    }))
    expect(result).toEqual({ status: 'error', message: 'channel closed' })
  })

  it('reports `gone` for the optimistic-local seq sentinel, without fetching', async () => {
    // Seq 0 means the row was never persisted, so no worker message can carry
    // it. That is not an error state to retry.
    const fetchMessage = vi.fn(async () => undefined)
    expect(await resolveChatImage({ ...ref, seq: 0n }, testMessageContext({ fetchMessage })))
      .toEqual({ status: 'gone' })
    expect(fetchMessage).not.toHaveBeenCalled()
  })
})
