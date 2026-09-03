import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import './providers/claude'
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

function deps(overrides: Partial<Parameters<typeof resolveChatImage>[1]> = {}) {
  return {
    getLoadedMessageBySeq: () => undefined,
    fetchMessageBySeq: async () => undefined,
    ...overrides,
  }
}

const ref = { workerId: 'w1', agentId: 'a1', seq: 7n, imageIndex: 0 }

describe('messagetoolresultimages', () => {
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

describe('imagefrommessage', () => {
  it('picks the image at the index', () => {
    expect(imageFromMessage(claudeImageMessage(['first', 'second']), 1)?.data).toBe('second')
  })

  it('is null when the index is past the end', () => {
    expect(imageFromMessage(claudeImageMessage(['only']), 3)).toBeNull()
  })
})

describe('resolvechatimage', () => {
  it('resolves from the loaded window without fetching', async () => {
    const fetchMessageBySeq = vi.fn(async () => undefined)
    const result = await resolveChatImage(ref, deps({
      getLoadedMessageBySeq: () => claudeImageMessage([PNG]),
      fetchMessageBySeq,
    }))
    expect(result).toEqual({ status: 'ready', source: { data: PNG, mimeType: 'image/png' } })
    expect(fetchMessageBySeq).not.toHaveBeenCalled()
  })

  it('fetches when the message is outside the loaded window', async () => {
    const fetchMessageBySeq = vi.fn(async () => claudeImageMessage([PNG]))
    const result = await resolveChatImage(ref, deps({ fetchMessageBySeq }))
    expect(result.status).toBe('ready')
    expect(fetchMessageBySeq).toHaveBeenCalledWith('w1', 'a1', 7n)
  })

  it('reports `gone` for a message the worker no longer has', async () => {
    // A definitive absence: the row was deleted or the seqs moved. Retrying
    // cannot help, and the tab has to say so rather than spin.
    expect(await resolveChatImage(ref, deps())).toEqual({ status: 'gone' })
  })

  it('reports `gone` when the message exists but holds no image at that index', async () => {
    const result = await resolveChatImage({ ...ref, imageIndex: 4 }, deps({
      getLoadedMessageBySeq: () => claudeImageMessage([PNG]),
    }))
    expect(result).toEqual({ status: 'gone' })
  })

  it('reports a retryable error when the fetch itself fails', async () => {
    const result = await resolveChatImage(ref, deps({
      fetchMessageBySeq: async () => {
        throw new Error('channel closed')
      },
    }))
    expect(result).toEqual({ status: 'error', message: 'channel closed' })
  })

  it('reports `gone` for the optimistic-local seq sentinel, without fetching', async () => {
    // Seq 0 means the row was never persisted, so no worker message can carry
    // it. That is not an error state to retry.
    const fetchMessageBySeq = vi.fn(async () => undefined)
    expect(await resolveChatImage({ ...ref, seq: 0n }, deps({ fetchMessageBySeq })))
      .toEqual({ status: 'gone' })
    expect(fetchMessageBySeq).not.toHaveBeenCalled()
  })
})
