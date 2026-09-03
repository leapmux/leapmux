import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pngBase64 } from '~/test-support/pngFixture'
import './claude'
import './codex'
import './opencode'
import './pi'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
  makeKey: (lang: string, code: string) => `${lang}\0${code}`,
}))

const { renderMessageContent } = await import('../messageRenderers')

const PNG = 'iVBORw0KGgo='
const PNG_DATA_URL = `data:image/png;base64,${PNG}`

function parsed(parentObject: Record<string, unknown>) {
  return { rawText: JSON.stringify(parentObject), topLevel: parentObject, parentObject, wrapper: null }
}

function renderToolResult(provider: AgentProvider, payload: unknown, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  return render(() => renderMessageContent(payload, context, category, provider))
}

function renderToolUse(provider: AgentProvider, toolUse: Record<string, unknown>, toolName: string, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_use', toolName, toolUse, content: [] }
  return render(() => renderMessageContent(toolUse, context, category, provider))
}

// ---------------------------------------------------------------------------
// Claude
// ---------------------------------------------------------------------------

function claudeToolResult(content: unknown, toolUseResult?: Record<string, unknown>, isError = false) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content, ...(isError ? { is_error: true } : {}) }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

describe('claude Read on an image', () => {
  // The reported bug: the row printed `![image](data:image/png;base64,…)` as
  // literal text inside the Read body's <pre>.
  it('renders an img instead of a base64 markdown string', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }],
      { type: 'image', file: { base64: PNG, type: 'image/png', originalSize: 136311 } },
    ), { spanType: 'Read' } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
    expect(container.textContent ?? '').not.toContain('base64,')
  })

  it('reserves the box from the DISPLAY dimensions, which describe the bytes sent', () => {
    // Claude downsamples before sending, so `originalWidth/Height` describe a
    // picture nobody receives. Reserving from those would mis-size exactly the
    // large screenshots the reservation exists for.
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }],
      {
        type: 'image',
        file: {
          base64: PNG,
          type: 'image/png',
          dimensions: { originalWidth: 2480, originalHeight: 2400, displayWidth: 620, displayHeight: 600 },
        },
      },
    ), { spanType: 'Read' } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('style') ?? '').toContain('620 / 600')
  })

  it('renders a Bash result whose stdout was a data URI as an image', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }],
    ), { spanType: 'Bash' } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
  })

  // A per-tool entry renders TEXT and never pixels, and `splitToolResultContent`
  // takes the images out of that text. Before the split they travelled inside it
  // as `![image](data:...)` and each entry's Markdown body drew them; afterwards
  // only the catch-all read `images`, so every tool with its own renderer --
  // Task/Agent, TaskOutput, WebFetch and nine more -- dropped the picture with no
  // placeholder and no log. Pi's renderer mounts the list beside its dispatch for
  // exactly this reason; Claude now does too.
  it('renders an image a subagent returned, whose tool has its own renderer', () => {
    // `Agent` has an entry in TOOL_RESULT_ENTRIES and `AgentResultView` renders
    // text only, so this is the path the catch-all never reaches.
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }],
      { status: 'completed', description: 'screenshot the page', content: 'done' },
    ), { spanType: 'Agent' } as RenderContext)
    expect(container.textContent ?? '').toContain('screenshot the page')
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
  })

  it('renders text and image together when a tool returns both', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult([
      { type: 'text', text: 'captured the page' },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } },
    ]), { spanType: 'mcp__playwright__screenshot' } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
    expect(container.textContent ?? '').toContain('captured the page')
  })
})

// ---------------------------------------------------------------------------
// Codex
// ---------------------------------------------------------------------------

describe('codex image items', () => {
  it('renders the generated image and the prompt it was rendered from', () => {
    const item = {
      type: 'imageGeneration',
      id: 'gen-1',
      status: 'completed',
      revisedPrompt: 'a red bicycle, studio lighting',
      result: PNG,
    }
    const { container } = renderToolUse(AgentProvider.CODEX, { item, threadId: 't1' }, 'imageGeneration')
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
    expect(container.textContent ?? '').toContain('a red bicycle, studio lighting')
  })

  it('shows the header alone while generation is in progress (result is empty)', () => {
    const item = { type: 'imageGeneration', id: 'gen-1', status: 'in_progress', result: '' }
    const { container } = renderToolUse(AgentProvider.CODEX, { item, threadId: 't1' }, 'imageGeneration')
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent ?? '').toContain('Generate image')
  })

  it('surfaces the failure kind when generation failed', () => {
    const item = {
      type: 'imageGeneration',
      id: 'gen-1',
      status: 'failed',
      result: '',
      failure: { type: 'usageLimitExceeded', limitId: 'images', resetsAt: null },
    }
    const { container } = renderToolUse(AgentProvider.CODEX, { item, threadId: 't1' }, 'imageGeneration')
    expect(container.textContent ?? '').toContain('usageLimitExceeded')
  })

  it('states the file for imageView, which carries no pixels', () => {
    const item = { type: 'imageView', id: 'view-1', path: '/repo/shot.png' }
    const { container } = renderToolUse(AgentProvider.CODEX, { item, threadId: 't1' }, 'imageView')
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent ?? '').toContain('shot.png')
  })
})

// ---------------------------------------------------------------------------
// ACP (OpenCode / Kilo / Goose)
// ---------------------------------------------------------------------------

describe('an ACP tool_call_update with image content', () => {
  // Regression: `flattenAcpContent` kept a `{type:'content', content}` wrapper
  // only when the inner block was text, so every ACP image was dropped before
  // any renderer saw it.
  it('renders an image the agent nested under a content wrapper', () => {
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      kind: 'read',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'image', mimeType: 'image/png', data: PNG } }],
    }
    const { container } = renderToolUse(AgentProvider.OPENCODE, toolUse, 'read')
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
  })

  it('renders the text body and the image from the same update', () => {
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      kind: 'read',
      status: 'completed',
      content: [
        { type: 'content', content: { type: 'text', text: 'read shot.png' } },
        { type: 'content', content: { type: 'image', mimeType: 'image/png', data: PNG } },
      ],
    }
    const { container } = renderToolUse(AgentProvider.OPENCODE, toolUse, 'read')
    expect(container.querySelector('img')).not.toBeNull()
    expect(container.textContent ?? '').toContain('read shot.png')
    expect(container.textContent ?? '').not.toContain('base64,')
  })
})

// ---------------------------------------------------------------------------
// Pi
// ---------------------------------------------------------------------------

describe('pi read on an image', () => {
  it('renders an img for the image content block', () => {
    const payload = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'read',
      result: { content: [{ type: 'image', data: PNG, mimeType: 'image/png' }] },
    }
    const start = { type: 'tool_execution_start', toolCallId: 'call-1', toolName: 'read', args: { filePath: '/repo/shot.png' } }
    const { container } = renderToolResult(AgentProvider.PI, payload, {
      spanType: 'read',
      toolUseParsed: parsed(start),
    } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('src')).toBe(PNG_DATA_URL)
    expect(container.textContent ?? '').not.toContain('base64,')
  })
})

// ---------------------------------------------------------------------------
// Guardrails shared by every provider
// ---------------------------------------------------------------------------

describe('shared image guardrails', () => {
  // SVG DRAWS. Every consumer mounts through `ImageRender`, which builds a blob
  // URL for an `<img>`, and an `<img>` renders SVG in secure static mode: no
  // script, no external fetch. The file viewer already drew on-disk SVGs the
  // same way, so refusing an agent's cost the diagram and bought nothing.
  it('draws an SVG, the same way the file viewer renders one off disk', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/svg+xml', data: 'PHN2Zy8+' } }],
    ), { spanType: 'Read' } as RenderContext)
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('refuses a type no `<img>` can draw', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'application/pdf', data: 'JVBERi0=' } }],
    ), { spanType: 'Read' } as RenderContext)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent ?? '').toContain('unsupported format')
  })

  it('reserves the intrinsic box by sniffing when the provider states no dimensions', () => {
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: pngBase64(640, 480) } }],
    ), { spanType: 'Bash' } as RenderContext)
    expect(container.querySelector('img')?.getAttribute('data-size-reserved')).toBe('1')
  })
})

// ---------------------------------------------------------------------------
// `Provider.toolResultImages` — the order an image tab addresses by index
// ---------------------------------------------------------------------------

const { pluginFor } = await import('./registry')

function imagesOf(provider: AgentProvider, parsedMessage: unknown, spanType?: string, toolUse?: Record<string, unknown>) {
  return pluginFor(provider)?.toolResultImages?.(parsedMessage, spanType, toolUse ? parsed(toolUse) : undefined) ?? []
}

describe('provider toolResultImages', () => {
  // A FAILED MCP call used to drop its whole content, images and all, because
  // the `error` string already carries the joined TEXT. Nothing else carries
  // the pixels, so a Playwright screenshot of the failure vanished from the
  // row -- and the row then showed fewer images than `toolResultImages`
  // numbers for that message, which is the index an open image tab addresses
  // by, permanently.
  it('claude keeps the images a failed MCP call returned, and the tab still numbers them', () => {
    const payload = claudeToolResult([
      { type: 'text', text: 'the tool failed' },
      { type: 'image', data: PNG, mimeType: 'image/png' },
    ], undefined, true)
    const { container } = renderToolResult(
      AgentProvider.CLAUDE_CODE,
      payload,
      { spanType: 'mcp__x__y' } as RenderContext,
    )
    expect(container.querySelectorAll('img')).toHaveLength(1)
    expect(imagesOf(AgentProvider.CLAUDE_CODE, payload, 'mcp__x__y')).toHaveLength(1)
  })

  it('claude keeps wire order across mixed blocks', () => {
    const images = imagesOf(AgentProvider.CLAUDE_CODE, claudeToolResult([
      { type: 'image', data: 'first', mimeType: 'image/png' },
      { type: 'text', text: 'between' },
      { type: 'image', data: 'second', mimeType: 'image/png' },
    ]), 'mcp__x__y')
    expect(images.map(i => i.data)).toEqual(['first', 'second'])
  })

  it('claude prefers the structured tool_use_result, which alone carries dimensions', () => {
    const images = imagesOf(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }],
      { type: 'image', file: { base64: PNG, type: 'image/png', dimensions: { displayWidth: 10, displayHeight: 20 } } },
    ), 'Read')
    expect(images).toEqual([{ data: PNG, mimeType: 'image/png', dimensions: { width: 10, height: 20 } }])
  })

  // The structured payload describes ONE image, so it can only stand in for the
  // blocks when the blocks are that same one. Letting it win here would drop the
  // second picture from the row AND shift every index an image tab addresses --
  // the invariant the whole reference scheme rests on.
  it('claude keeps every block when the result carries more images than the structured one', () => {
    const images = imagesOf(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [
        { type: 'image', data: 'first', mimeType: 'image/png' },
        { type: 'image', data: 'second', mimeType: 'image/png' },
      ],
      { type: 'image', file: { base64: PNG, type: 'image/png', dimensions: { displayWidth: 10, displayHeight: 20 } } },
    ), 'mcp__x__y')
    expect(images.map(i => i.data)).toEqual(['first', 'second'])
    expect(images.every(i => i.dimensions === undefined)).toBe(true)
  })

  // The zero-block case takes the same branch as the one-block case, so the
  // structured payload is still the answer: a Read on an image that sent no
  // content block at all is exactly what it describes.
  it('claude falls back to the structured payload when the blocks carry no image', () => {
    const images = imagesOf(AgentProvider.CLAUDE_CODE, claudeToolResult(
      [{ type: 'text', text: 'no image here' }],
      { type: 'image', file: { base64: PNG, type: 'image/png' } },
    ), 'Read')
    expect(images).toEqual([{ data: PNG, mimeType: 'image/png' }])
  })

  it('claude attaches the tool_use file path, which routes the click to the file itself', () => {
    const images = imagesOf(
      AgentProvider.CLAUDE_CODE,
      claudeToolResult([{ type: 'image', data: PNG, mimeType: 'image/png' }]),
      'Read',
      { type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Read', input: { file_path: '/repo/shot.png' } }] } },
    )
    expect(images[0]?.filePath).toBe('/repo/shot.png')
  })

  it('claude returns nothing for a message with no tool_result', () => {
    expect(imagesOf(AgentProvider.CLAUDE_CODE, { type: 'assistant', message: { content: [] } })).toEqual([])
    expect(imagesOf(AgentProvider.CLAUDE_CODE, null)).toEqual([])
  })

  it('an ACP update keeps wire order and falls back to the tool input path', () => {
    const images = imagesOf(AgentProvider.OPENCODE, {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      kind: 'read',
      status: 'completed',
      rawInput: { path: '/repo/shot.png' },
      content: [
        { type: 'content', content: { type: 'image', mimeType: 'image/png', data: 'first' } },
        { type: 'content', content: { type: 'text', text: 'between' } },
        { type: 'content', content: { type: 'image', mimeType: 'image/png', data: 'second', uri: 'file:///repo/other.png' } },
      ],
    })
    expect(images.map(i => i.data)).toEqual(['first', 'second'])
    // The block's own `uri` wins over the tool input, because it states the
    // file THIS image came from when a tool returned several.
    expect(images.map(i => i.filePath)).toEqual(['/repo/shot.png', '/repo/other.png'])
  })

  it('codex reads each of its three image-bearing item shapes', () => {
    expect(imagesOf(AgentProvider.CODEX, { item: { type: 'imageGeneration', result: PNG, savedPath: '/tmp/a.png' } }))
      .toEqual([{ data: PNG, mimeType: 'image/png', filePath: '/tmp/a.png' }])
    expect(imagesOf(AgentProvider.CODEX, { item: { type: 'mcpToolCall', result: { content: [{ type: 'image', data: PNG, mimeType: 'image/png' }] } } }))
      .toEqual([{ data: PNG, mimeType: 'image/png' }])
    expect(imagesOf(AgentProvider.CODEX, { item: { type: 'dynamicToolCall', contentItems: [{ type: 'inputImage', imageUrl: PNG_DATA_URL }] } }))
      .toEqual([{ url: PNG_DATA_URL }])
  })

  it('codex reports no image for imageView, which carries only a path', () => {
    expect(imagesOf(AgentProvider.CODEX, { item: { type: 'imageView', id: 'v1', path: '/repo/shot.png' } })).toEqual([])
  })

  it('pi keeps wire order and takes the path from the paired start event', () => {
    const images = imagesOf(
      AgentProvider.PI,
      { type: 'tool_execution_end', toolCallId: 'c1', toolName: 'read', result: { content: [
        { type: 'text', text: 'between' },
        { type: 'image', data: 'first', mimeType: 'image/png' },
        { type: 'image', data: 'second', mimeType: 'image/png' },
      ] } },
      'read',
      { type: 'tool_execution_start', toolCallId: 'c1', toolName: 'read', args: { filePath: '/repo/shot.png' } },
    )
    expect(images.map(i => i.data)).toEqual(['first', 'second'])
    expect(images.every(i => i.filePath === '/repo/shot.png')).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// The clipboard and the scroll rail must not carry the base64
// ---------------------------------------------------------------------------

const { claudeToolResultMeta } = await import('./claude/toolResult')

describe('claude tool-result toolbar with an image', () => {
  // Copying a `Read` on a screenshot used to put a megabyte of base64 on the
  // clipboard, because the copy text and the rendered body came from the same
  // Markdown-formatted join.
  it('copies the text beside the image, never the base64', () => {
    const meta = claudeToolResultMeta(
      { kind: 'tool_result' },
      claudeToolResult([
        { type: 'text', text: 'captured the page' },
        { type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } },
      ]),
      'mcp__playwright__screenshot',
      undefined,
    )
    const copied = meta?.copyableContent() ?? ''
    expect(copied).toContain('captured the page')
    expect(copied).not.toContain('base64,')
  })

  it('offers nothing to copy for an image-only result', () => {
    const meta = claudeToolResultMeta(
      { kind: 'tool_result' },
      claudeToolResult([{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: PNG } }]),
      'Read',
      undefined,
    )
    expect(meta?.copyableContent() ?? '').not.toContain('base64,')
  })
})

// ---------------------------------------------------------------------------
// Clicking an image
// ---------------------------------------------------------------------------

describe('opening a tool-result image', () => {
  it('reports the image index within the MESSAGE, not within the content items', () => {
    // The index is what an image tab addresses, and it must agree with
    // `toolResultImages`, which never sees the text blocks between the images.
    const onOpenImage = vi.fn()
    const { container } = renderToolResult(AgentProvider.CLAUDE_CODE, claudeToolResult([
      { type: 'text', text: 'first' },
      { type: 'image', data: PNG, mimeType: 'image/png' },
      { type: 'text', text: 'second' },
      { type: 'image', data: PNG, mimeType: 'image/png' },
    ]), { spanType: 'mcp__playwright__screenshot', onOpenImage } as unknown as RenderContext)

    const buttons = [...container.querySelectorAll('button')].filter(b => b.querySelector('img'))
    expect(buttons).toHaveLength(2)
    buttons[1]?.click()
    expect(onOpenImage).toHaveBeenCalledWith(expect.objectContaining({ index: 1 }))
  })

  it('reports the file path when the provider stated one, so the file itself opens', () => {
    const onOpenImage = vi.fn()
    const { container } = renderToolResult(
      AgentProvider.CLAUDE_CODE,
      claudeToolResult([{ type: 'image', data: PNG, mimeType: 'image/png' }]),
      {
        spanType: 'Read',
        onOpenImage,
        toolUseParsed: parsed({ type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Read', input: { file_path: '/repo/shot.png' } }] } }),
      } as unknown as RenderContext,
    )
    container.querySelector('button')?.click()
    expect(onOpenImage).toHaveBeenCalledWith(expect.objectContaining({ index: 0, filePath: '/repo/shot.png' }))
  })

  it('titles the tab the way the row titles itself, not the raw tool name', () => {
    // The MCP body already holds the provider-computed "Server / tool" pair,
    // so the tab reads "Playwright / screenshot" rather than the span type's
    // `mcp__playwright__screenshot`.
    const onOpenImage = vi.fn()
    const { container } = renderToolResult(
      AgentProvider.CLAUDE_CODE,
      claudeToolResult([{ type: 'image', data: PNG, mimeType: 'image/png' }]),
      { spanType: 'mcp__playwright__screenshot', onOpenImage } as unknown as RenderContext,
    )
    container.querySelector('button')?.click()
    expect(onOpenImage).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'Playwright / screenshot' }),
    )
  })

  it('lets an ACP row name the tab from its own description', () => {
    const onOpenImage = vi.fn()
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      kind: 'read',
      status: 'completed',
      rawInput: { description: 'Screenshot the login page' },
      content: [{ type: 'content', content: { type: 'image', mimeType: 'image/png', data: PNG } }],
    }
    const { container } = renderToolUse(AgentProvider.OPENCODE, toolUse, 'read', { onOpenImage } as unknown as RenderContext)
    const imageButton = [...container.querySelectorAll('button')].find(b => b.querySelector('img'))
    imageButton?.click()
    expect(onOpenImage).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'Screenshot the login page' }),
    )
  })

  it('leaves the title to the bubble when the renderer has no better name', () => {
    // A bare Claude tool result: the catch-all body knows only what the row
    // knows, so it says nothing and MessageBubble fills in the span type.
    const onOpenImage = vi.fn()
    const { container } = renderToolResult(
      AgentProvider.CLAUDE_CODE,
      claudeToolResult([{ type: 'image', data: PNG, mimeType: 'image/png' }]),
      { spanType: 'Bash', onOpenImage } as unknown as RenderContext,
    )
    container.querySelector('button')?.click()
    expect(onOpenImage).toHaveBeenCalledWith(
      expect.objectContaining({ title: undefined }),
    )
  })

  it('renders no button at all when the host offers no way to open one', () => {
    // A button that does nothing is worse than no button, and it would take a
    // tab stop on every image in the transcript.
    const { container } = renderToolResult(
      AgentProvider.CLAUDE_CODE,
      claudeToolResult([{ type: 'image', data: PNG, mimeType: 'image/png' }]),
      { spanType: 'Read' } as RenderContext,
    )
    expect(container.querySelector('img')).not.toBeNull()
    expect(container.querySelector('button')).toBeNull()
  })

  it('reports the same index for an ACP image', () => {
    const onOpenImage = vi.fn()
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      kind: 'read',
      status: 'completed',
      content: [
        { type: 'content', content: { type: 'text', text: 'read them' } },
        { type: 'content', content: { type: 'image', mimeType: 'image/png', data: PNG } },
        { type: 'content', content: { type: 'image', mimeType: 'image/png', data: PNG } },
      ],
    }
    const { container } = renderToolUse(AgentProvider.OPENCODE, toolUse, 'read', { onOpenImage } as unknown as RenderContext)
    // Scoped to the buttons that WRAP an image: the row also carries an
    // expand toggle, and picking by position would silently start asserting
    // about that one the day the header grows another control.
    const imageButtons = [...container.querySelectorAll('button')].filter(b => b.querySelector('img'))
    expect(imageButtons).toHaveLength(2)
    imageButtons[1]?.click()
    expect(onOpenImage).toHaveBeenCalledWith(expect.objectContaining({ index: 1 }))
  })
})
