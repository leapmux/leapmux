import { describe, expect, it } from 'vitest'
import { resolveDeepseekHarnessMessage } from './resolveMessage'

describe('resolveDeepseekHarnessMessage', () => {
  const parentObject = { type: 'assistant/message', data: { message: { content: [{ type: 'text', text: 'Answer' }] } } }
  const parsed = { topLevel: parentObject, parentObject, wrapper: null, rawText: '' }

  it('merges a zero block identity into a separate display copy', () => {
    const supplementalContent = { blockIndex: 0 }
    expect(resolveDeepseekHarnessMessage({ ...parsed, supplementalContent })).toEqual({ ...parentObject, blockIndex: 0 })
    expect(parentObject).not.toHaveProperty('blockIndex')
    expect(supplementalContent).toEqual({ blockIndex: 0 })
  })

  it.each([{ index: undefined }, { index: null }, { index: -1 }, { index: 0.5 }, { index: '0' }])('rejects an invalid supplemental index: $index', ({ index }) => {
    expect(resolveDeepseekHarnessMessage({ ...parsed, supplementalContent: { blockIndex: index } })).toBeUndefined()
  })

  it('does not apply the block supplement to a tool result', () => {
    expect(resolveDeepseekHarnessMessage({ ...parsed, parentObject: { type: 'tool/result', data: {} }, supplementalContent: { blockIndex: 0 } })).toBeUndefined()
  })

  it('keeps the native preview beside authenticated omitted images', () => {
    const attachment = { attachmentId: `sha256:${'a'.repeat(64)}`, mediaType: 'image/png', bytes: 168, width: 64, height: 64 }
    const data = btoa('\0'.repeat(168))
    const content = [{ type: 'text', text: 'The retained native excerpt.' }]
    const original = { type: 'tool/result', data: { message: { toolCallId: 'call', content, isError: false } } }
    const supplementalContent = {
      imageAttachments: { sessionId: 'session', toolCallId: 'call', originalImages: [{ position: 1, attachment }], retainedImages: [], images: { [attachment.attachmentId]: { attachment, data } } },
    }
    expect(resolveDeepseekHarnessMessage({ topLevel: original, parentObject: original, rawText: '', wrapper: null, agentSessionId: 'session', supplementalContent }))
      .toMatchObject({ data: { message: { content: [content[0], { type: 'image', attachment, mimeType: 'image/png', data }] } } })
    expect(original.data.message.content).toEqual(content)
  })
})
