import type { MessageContentRenderContext } from '../../messageContentRenderer'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { prettifyJson } from '~/lib/jsonFormat'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { pngBase64 } from '~/test-support/pngFixture'
import { providerRowImages } from '~/test-support/toolCallFixture'
import { imageActionsFrom } from '../../renderContext'
import { toolOutcomeLabel } from '../../results/toolOutcomeLabel'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { renderZCodeRow, ZCodeRowView } from './testUtils'
import './plugin'
import '../testMocks'

const PNG = pngBase64(2, 3)

function result(display: Record<string, unknown>, content = '') {
  return { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'tool', result: { success: true, content, display } } }
}

function renderDisplay(display: Record<string, unknown>, context?: MessageContentRenderContext, content = '') {
  const parsed = result(display, content)
  return { ...renderZCodeRow(parsed, context), parsed }
}

describe('zcode result display hints', () => {
  it('formats argument and structured JSON strings without changing the source', () => {
    const args = '{"values":[1,2],"count":900719925474099312345}'
    const structured = '{"count":0,"enabled":false}'
    const display = { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup', input: args, structuredContent: structured }
    const { container } = renderDisplay(display, { getMessageUiState: () => true })
    expect(container.textContent).toContain(prettifyJson(args))
    expect(container.textContent).toContain(prettifyJson(structured))
    expect(container.textContent).toContain('900719925474099312345')
    expect(display.input).toBe(args)
    expect(display.structuredContent).toBe(structured)
  })

  it('renders stored MCP attachments in their original content order', () => {
    const parsed = result({ kind: 'mcp_tool', serverName: 'docs', toolName: 'read' }, '[Attached image/png: MCP image]')
    const uri = 'zcode-artifact://session/tool-result-image'
    const current = input(parsed)
    current.supplementalContent = {
      type: 'tool.updated',
      payload: { kind: 'result', toolCallId: 'tool' },
      nativeTool: {
        id: 'part',
        sessionId: 'session',
        messageId: 'message',
        data: {
          type: 'tool',
          callID: 'tool',
          tool: 'mcp__docs__read',
          state: {
            status: 'completed',
            input: { topic: 'retained input' },
            metadata: { modelContentLayout: [{ type: 'text', text: 'Before image' }, { type: 'attachment', attachmentIndex: 0 }, { type: 'text', text: 'After image' }] },
            attachments: [{ type: 'file', sessionID: 'session', messageID: 'message', mime: 'image/png', filename: 'MCP image', url: uri }],
          },
        },
      },
      artifacts: { [uri]: `data:image/png;base64,${PNG}` },
    }
    const context = { spanType: 'mcp__docs__read', sources: testMessageSources({ current: () => current }) }
    const { container } = renderZCodeRow(parsed, context)
    expect(container.querySelectorAll('img')).toHaveLength(1)
    expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${PNG}`)
    expect(container.textContent).toContain('Before image')
    expect(container.textContent).toContain('After image')
    expect(container.textContent).toContain('retained input')
    expect(container.textContent).not.toContain('[Attached image')
    const images = providerRowImages(AgentProvider.ZCODE, parsed, {
      spanType: context.spanType,
      supplementalContent: current.supplementalContent,
    })
    expect(images).toHaveLength(1)
    expect(images[0]?.url).toBe(`data:image/png;base64,${PNG}`)
    const request = { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'tool', toolName: 'mcp__docs__read', inputOmitted: true } }
    const requestParsed = input(request)
    const header = render(() => <ZCodeRowView parsed={request} context={{ spanType: context.spanType, sources: testMessageSources({ current: () => requestParsed, result: () => current }) }} />)
    expect(header.container.textContent).toContain('retained input')
  })

  it('recovers a read header from a native result when streamed arguments are absent', () => {
    const request = { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read', toolName: 'Read', inputOmitted: true } }
    const requestParsed = input(request)
    const completed = input({ type: 'tool.updated', payload: { kind: 'result', toolCallId: 'read', result: { success: true, content: 'file content' } } })
    completed.supplementalContent = {
      type: 'tool.updated',
      payload: { kind: 'result', toolCallId: 'read' },
      nativeTool: { id: 'part', sessionId: 'session', messageId: 'message', data: { type: 'tool', callID: 'read', tool: 'Read', state: { status: 'completed', input: { file_path: '/project/recovered.ts' } } } },
    }
    const plugin = providerFor(AgentProvider.ZCODE)!
    expect(plugin?.transcript.relatedMessages?.(requestParsed)).toEqual(['result'])
    const { container } = render(() => <ZCodeRowView parsed={request} context={{ spanType: 'Read', sources: testMessageSources({ current: () => requestParsed, request: () => requestParsed, result: () => completed }) }} />)
    expect(container.textContent).toContain('recovered.ts')
  })

  it('shows a failed MCP display status without an error code', () => {
    const { container } = renderDisplay({ kind: 'mcp_tool', serverName: 'docs', toolName: 'read', status: 'failed' })
    expect(container.textContent).toContain(toolOutcomeLabel('failed'))
  })

  it('renders node images and exposes the same sources to the image viewer', () => {
    const { container, parsed } = renderDisplay({ kind: 'node_repl_images', images: [{ base64: PNG, mimeType: 'image/png' }] })
    expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${PNG}`)
    expect(providerRowImages(AgentProvider.ZCODE, parsed, { spanType: 'js' })).toEqual([{ data: PNG, mimeType: 'image/png' }])
  })

  it('keeps image indices when computer-use media omits the first image data', () => {
    const onOpenImage = vi.fn()
    // `openImage` is set, so the assembled actions are never undefined.
    const images = imageActionsFrom({ openImage: onOpenImage })
    const { container, parsed } = renderDisplay({
      kind: 'cua',
      toolName: 'screenshot',
      status: 'success',
      text: 'Screenshot captured',
      media: [{ mimeType: 'image/png', artifactUri: 'artifact://first' }, { mimeType: 'image/png', data: PNG }],
    // `openImage` is set, so the assembled actions are never undefined.
    }, images ? { images } : {})
    expect(container.querySelectorAll('img')).toHaveLength(1)
    const button = container.querySelector('[aria-label="Open image"]')
    expect(button).not.toBeNull()
    fireEvent.click(button!)
    expect(onOpenImage).toHaveBeenCalledWith(expect.objectContaining({ index: 1 }))
    expect(providerRowImages(AgentProvider.ZCODE, parsed)).toHaveLength(2)
    expect(container.textContent).toContain('Screenshot captured')
  })

  it('reports omitted images when the provider truncates a display', () => {
    const { container } = renderDisplay({ kind: 'node_repl_images', images: [{ base64: PNG, mimeType: 'image/png' }], truncated: true })
    expect(container.textContent?.toLowerCase()).toContain('truncated')
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('renders MCP identity and recovers arguments from the request', () => {
    const request = input({ type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'tool', toolName: 'mcp__docs__lookup', input: { query: 'renderer' } } })
    const parsed = result({ kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' }, 'The tool response')
    const { container } = render(() => [
      <ZCodeRowView parsed={request.parentObject} context={{ sources: testMessageSources({ current: () => request, result: () => input(parsed) }) }} />,
      <ZCodeRowView parsed={parsed} context={{ sources: testMessageSources({ request: () => request }) }} />,
    ])
    expect(container.textContent).toContain('docs / lookup')
    expect(container.textContent).toContain('Arguments')
    expect(container.textContent).toContain('renderer')
    expect(container.textContent).toContain('The tool response')
  })

  it('renders task output and its state when the text result is empty', () => {
    const { container } = renderDisplay({ kind: 'task_output', retrievalStatus: 'not_ready', taskStatus: 'running', output: 'background output marker' })
    expect(container.textContent).toContain('background output marker')
    expect(container.textContent).toContain('Running')
  })

  it('renders a stopped task with its command and identifier', () => {
    const { container } = renderDisplay({ kind: 'task_stop', taskId: 'task-42', taskType: 'shell', command: 'npm run dev', message: 'Stopped the task' })
    expect(container.textContent).toContain('task-42')
    expect(container.textContent).toContain('npm run dev')
    expect(container.textContent).toContain('Stopped the task')
  })

  it('preserves a local message failure and its error', () => {
    const { container } = renderDisplay({ kind: 'local_agent_message', status: 'failed', error: 'Peer unavailable' })
    expect(container.textContent).toContain('Peer unavailable')
    // A message-send status, not a tool outcome. Its success word is `Message sent`, so
    // its failure word pairs with that rather than with the shared tool vocabulary.
    expect(container.textContent).toContain('Failed')
  })

  it('renders coordinator response status without a text result', () => {
    const { container } = renderDisplay({ kind: 'respond_to_coordinator', status: 'success' })
    expect(container.textContent).toContain('Message sent')
  })

  it('preserves the text result for an unknown display kind', () => {
    const { container } = renderDisplay({ kind: 'new_display' }, undefined, 'Fallback result marker')
    expect(container.textContent).toContain('Fallback result marker')
  })
})
