import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')
type RenderContext = import('../messageRenderers').RenderContext

/** Construct a Glob tool_use assistant message. */
function makeGlobToolUse(input: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-glob',
        name: 'Glob',
        input: { pattern: 'frontend/src/**/*.test.*', ...input },
      }],
    },
  }
}

/** Construct a Glob tool_result user message with tool_use_result. */
function makeGlobToolResult(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-glob',
        type: 'tool_result',
        content: resultContent,
      }],
    },
    tool_use_result: toolUseResult,
  }
}

/** Render a Glob tool_use message and return its text content. */
function renderToolUseText(context?: RenderContext): string {
  const msg = makeGlobToolUse()
  const toolUse = (msg.message.content as Array<Record<string, unknown>>)[0]
  const category: MessageCategory = { kind: 'tool_use', toolName: 'Glob', toolUse, content: msg.message.content as Array<Record<string, unknown>> }
  const result = renderMessageContent(msg, MessageRole.ASSISTANT, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a Glob tool_result message and return its container element. */
function renderToolResultContainer(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): HTMLElement {
  const msg = makeGlobToolResult(resultContent, toolUseResult)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(msg, MessageRole.USER, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container
}

/** Render a Glob tool_result message and return its text content. */
function renderToolResultText(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): string {
  return renderToolResultContainer(resultContent, toolUseResult, context).textContent?.trim() ?? ''
}

describe('glob tool_use collapsed summary', () => {
  it('shows pattern in header', () => {
    const text = renderToolUseText()
    expect(text).toContain('frontend/src/**/*.test.*')
  })

  it('shows only the pattern with no summary (child data fields removed)', () => {
    const text = renderToolUseText()
    expect(text).toContain('frontend/src/**/*.test.*')
    expect(text).not.toContain('Found')
    expect(text).not.toContain('No files')
  })
})

describe('glob tool_result expanded view', () => {
  it('shows file list with relativized paths', () => {
    const text = renderToolResultText(
      '/home/user/project/src/foo.ts\n/home/user/project/src/bar.ts',
      {
        tool_name: 'Glob',
        filenames: ['/home/user/project/src/foo.ts', '/home/user/project/src/bar.ts'],
        numFiles: 2,
        durationMs: 500,
        truncated: false,
      },
      { workingDir: '/home/user/project' },
    )
    expect(text).toContain('src/foo.ts')
    expect(text).toContain('src/bar.ts')
    expect(text).not.toContain('/home/user/project')
  })

  it('shows "No files found" when filenames is empty', () => {
    const text = renderToolResultText('No files found', {
      tool_name: 'Glob',
      filenames: [],
      numFiles: 0,
      truncated: false,
    })
    expect(text).toContain('No files found')
  })

  it('shows fallback content when filenames is empty with custom message', () => {
    const text = renderToolResultText('No matching files in directory', {
      tool_name: 'Glob',
      filenames: [],
      numFiles: 0,
      truncated: false,
    })
    expect(text).toContain('No matching files in directory')
  })

  it('falls back to raw preformatted text when tool_use_result is missing', () => {
    const text = renderToolResultText(
      '/home/user/project/src/foo.ts\n/home/user/project/src/bar.ts',
      undefined,
      { spanType: 'Glob' },
    )
    expect(text).toContain('/home/user/project/src/foo.ts')
    expect(text).toContain('/home/user/project/src/bar.ts')
  })

  it('renders single file correctly', () => {
    const text = renderToolResultText(
      'src/only-file.ts',
      {
        tool_name: 'Glob',
        filenames: ['src/only-file.ts'],
        numFiles: 1,
        truncated: false,
      },
    )
    expect(text).toContain('only-file.ts')
  })
})
