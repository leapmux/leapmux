import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { MAX_INLINE_IMAGE_BASE64_LEN } from '~/lib/imageBlocks'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { ChatImageViewer, decodeImageBytes } from './ChatImageViewer'
import './providers/claude'
import './providers/testMocks'

// A one-pixel PNG's first bytes; only the decode matters here, not the image.
const PNG_BASE64 = 'iVBORw0KGgo='
const PNG_BYTES = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]

describe('decodeImageBytes', () => {
  it('decodes inline base64 with its stated mime type', () => {
    const decoded = decodeImageBytes({ data: PNG_BASE64, mimeType: 'image/png' })
    expect(decoded?.mimeType).toBe('image/png')
    expect([...(decoded?.content ?? [])]).toEqual(PNG_BYTES)
  })

  it('decodes a pre-formed data URL, taking the mime from the URL', () => {
    const decoded = decodeImageBytes({ url: `data:image/jpeg;base64,${PNG_BASE64}` })
    expect(decoded?.mimeType).toBe('image/jpeg')
    expect([...(decoded?.content ?? [])]).toEqual(PNG_BYTES)
  })

  it('refuses base64 with no mime type, which no blob could be built from', () => {
    expect(decodeImageBytes({ data: PNG_BASE64 })).toBeNull()
  })

  it('refuses an external URL: nothing local to open, and fetching would leak', () => {
    expect(decodeImageBytes({ url: 'https://example.com/x.png' })).toBeNull()
  })

  it('refuses a non-base64 data URL', () => {
    expect(decodeImageBytes({ url: 'data:image/png,rawbytes' })).toBeNull()
  })

  // The tab addresses its image by (agent, seq, index) and re-resolves it at
  // open time, so the source it decodes is not necessarily the one the click
  // validated -- a same-seq message merge can move index N. Applying the SAME
  // policy the transcript row applies is what keeps the two from disagreeing.
  it('decodes an SVG, which the transcript row draws and the file viewer already did', () => {
    const decoded = decodeImageBytes({ data: PNG_BASE64, mimeType: 'image/svg+xml' })
    expect(decoded?.mimeType).toBe('image/svg+xml')
  })

  it('refuses a type the row refuses, so the two cannot disagree', () => {
    expect(decodeImageBytes({ data: PNG_BASE64, mimeType: 'application/pdf' })).toBeNull()
  })

  it('refuses a payload past the inline size cap', () => {
    const huge = 'A'.repeat(MAX_INLINE_IMAGE_BASE64_LEN + 1)
    expect(decodeImageBytes({ data: huge, mimeType: 'image/png' })).toBeNull()
  })

  // A parameter must not reach the Blob type, or the tab and the row describe
  // the same bytes two ways.
  it('drops a data-URL parameter from the mime it hands the blob', () => {
    const decoded = decodeImageBytes({ url: `data:image/png;charset=utf-8;base64,${PNG_BASE64}` })
    expect(decoded?.mimeType).toBe('image/png')
  })

  it('refuses a data URL with no comma', () => {
    expect(decodeImageBytes({ url: 'data:image/png;base64' })).toBeNull()
  })

  it('returns null rather than throwing on undecodable base64', () => {
    expect(decodeImageBytes({ data: '!!!not base64!!!', mimeType: 'image/png' })).toBeNull()
  })

  it('returns null for a source with nothing in it', () => {
    expect(decodeImageBytes({})).toBeNull()
  })
})

/**
 * One tool_result carrying `blocks` as its content, as Claude spells it.
 *
 * Built through the real message factory and read back through the real Claude
 * plugin, so the viewer resolves its index exactly as the chat row produced it.
 * A hand-built `ImageResultSource` would prove only that the Switch below works
 * on a shape nothing sends.
 */
function claudeImageMessage(blocks: unknown[], seq = 7n): AgentChatMessage {
  return makeMessage({
    seq,
    spanType: 'mcp__playwright__screenshot',
    agentProvider: AgentProvider.CLAUDE_CODE,
    content: rawContent({
      type: 'user',
      message: {
        role: 'user',
        content: [{ type: 'tool_result', tool_use_id: 'r1', content: blocks }],
      },
    }),
  })
}

function renderViewer(message: AgentChatMessage | undefined) {
  const fetchMessageBySeq = vi.fn(async () => undefined)
  const result = render(() => (
    <ChatImageViewer
      workerId="w-1"
      agentId="a-1"
      seq={7n}
      imageIndex={0}
      title="screenshot"
      deps={{ getLoadedMessageBySeq: () => message, fetchMessageBySeq }}
    />
  ))
  return { ...result, fetchMessageBySeq }
}

describe('chatImageViewer', () => {
  it('draws the image the reference points at, resolved from the loaded window', async () => {
    const { container, fetchMessageBySeq } = renderViewer(
      claudeImageMessage([{ type: 'image', data: PNG_BASE64, mimeType: 'image/png' }]),
    )

    const img = await waitFor(() => {
      const el = container.querySelector('img')
      expect(el).not.toBeNull()
      return el!
    })
    // A blob URL, never the base64: the pixels reach the DOM as an object URL
    // this component owns and revokes, not as a megabyte-long attribute.
    expect(img.getAttribute('src')).toMatch(/^blob:/)
    expect(img.getAttribute('alt')).toBe('screenshot')
    // The message was already in the window, so nothing was fetched.
    expect(fetchMessageBySeq).not.toHaveBeenCalled()
  })

  it('says so when the message no longer holds an image at that index', async () => {
    const { container } = renderViewer(claudeImageMessage([{ type: 'text', text: 'no image' }]))
    await waitFor(() => {
      expect(container.textContent).toContain('no longer in the conversation')
    })
    expect(container.querySelector('img')).toBeNull()
  })

  // The guard branch. An image block can be legitimate and carry nothing -- an
  // Anthropic `source:{type:'file'}`, or an MCP server that states a type and
  // no payload -- and it keeps its index so the row and this tab still agree.
  // Without the branch this resolves `ready`, decodes to nothing, and the tab sits
  // on "Loading image…" forever.
  it('says so when the reference resolves to bytes it cannot decode', async () => {
    const { container } = renderViewer(claudeImageMessage([{ type: 'image', mimeType: 'image/png' }]))
    await waitFor(() => {
      expect(container.textContent).toContain('cannot be displayed')
    })
    expect(container.textContent).not.toContain('Loading image')
    expect(container.querySelector('img')).toBeNull()
  })
})
