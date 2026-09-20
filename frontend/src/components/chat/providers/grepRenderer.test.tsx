import type { MessageCategory } from '../messageClassifier'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import './testMocks'

const { renderMessageContent } = await import('../messageContentRenderer')
type MessageContentRenderContext = import('../messageContentRenderer').MessageContentRenderContext

/** Construct a Grep tool_use assistant message. */
function makeGrepToolUse(input: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-grep',
        name: 'Grep',
        input: { pattern: 'ToolHeaderActions', ...input },
      }],
    },
  }
}

/** Construct a Grep tool_result user message with tool_use_result. */
function makeGrepToolResult(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-grep',
        type: 'tool_result',
        content: resultContent,
      }],
    },
    tool_use_result: toolUseResult,
  }
}

/** Render a Grep tool_use message and return its text content. */
function renderToolUseText(context?: MessageContentRenderContext): string {
  const msg = makeGrepToolUse({ path: '/home/user/project/src' })
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(msg, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a Grep tool_result message and return its text content. */
function renderToolResultText(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: MessageContentRenderContext,
): string {
  const msg = makeGrepToolResult(resultContent, toolUseResult)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(msg, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

describe('grep tool_use collapsed summary', () => {
  it('shows pattern in header', () => {
    const text = renderToolUseText()
    expect(text).toContain('"ToolHeaderActions"')
  })

  it('shows path in summary (child data fields removed, no result counts)', () => {
    const text = renderToolUseText()
    expect(text).toContain('src')
    expect(text).not.toContain('Found')
    expect(text).not.toContain('No matches')
  })
})

describe('grep tool_result expanded view', () => {
  it('shows "No matches found" when numFiles and numLines are 0', () => {
    const text = renderToolResultText('No matches found', {
      tool_name: 'Grep',
      numFiles: 0,
      filenames: [],
      content: '',
      numLines: 0,
    })
    expect(text).toContain('No matches found')
  })

  /**
   * The STRUCTURED result decides, not the words beside it.
   *
   * This row drew the raw sentence instead of the marker while the renderer compared
   * `fallbackContent` against its own summary prose and fell through whenever the two
   * differed -- LeapMux's user-interface wording measured against a provider's bytes,
   * in the layer that knows no provider. The extractor states the fact now: a
   * `tool_use_result` whose every counter reads zero IS the tool reporting that it
   * matched nothing, whatever sentence it printed above.
   */
  it('draws the kind marker over the raw sentence when the counters state nothing', () => {
    const text = renderToolResultText('No files found', {
      tool_name: 'Grep',
      numFiles: 0,
      filenames: [],
      content: '',
      numLines: 0,
    })
    expect(text).toContain('No matches found')
    expect(text).not.toContain('No files found')
  })

  // The other half of the same rule. A structured answer that stated NO counter did
  // not report "nothing found" -- it reported nothing this build could read -- so the
  // raw text is still the only answer the row has, and it draws.
  it('keeps a body that no structured counter explains', () => {
    const text = renderToolResultText('the daemon wrote something else', { tool_name: 'Grep' })
    expect(text).toContain('the daemon wrote something else')
    expect(text).not.toContain('No matches found')
  })

  it('shows content when numLines > 0', () => {
    const matchContent = 'src/foo.ts\n42:const x = 1;\n43:const y = 2;'
    const text = renderToolResultText(matchContent, {
      tool_name: 'Grep',
      numFiles: 0,
      filenames: [],
      content: matchContent,
      numLines: 3,
    })
    expect(text).toContain('const x = 1;')
  })

  it('shows file list when numFiles > 0', () => {
    const text = renderToolResultText(
      'src/foo.ts\nsrc/bar.ts',
      {
        tool_name: 'Grep',
        numFiles: 2,
        filenames: ['src/foo.ts', 'src/bar.ts'],
        content: '',
        numLines: 0,
      },
    )
    expect(text).toContain('src/foo.ts')
    expect(text).toContain('src/bar.ts')
  })

  it('shows both file list and content when both numFiles and numLines > 0', () => {
    const matchContent = '42:const x = 1;'
    const text = renderToolResultText(
      'src/foo.ts\n42:const x = 1;',
      {
        tool_name: 'Grep',
        numFiles: 1,
        filenames: ['src/foo.ts'],
        content: matchContent,
        numLines: 1,
      },
    )
    expect(text).toContain('src/foo.ts')
    expect(text).toContain('const x = 1;')
  })

  it('falls back to raw preformatted text when tool_use_result is missing', () => {
    const text = renderToolResultText('raw grep output line 1\nline 2', undefined, {
      spanType: 'Grep',
    })
    expect(text).toContain('raw grep output line 1')
    expect(text).toContain('line 2')
  })

  it('relativizes file paths in file list', () => {
    const text = renderToolResultText(
      '/home/user/project/src/foo.ts',
      {
        tool_name: 'Grep',
        numFiles: 1,
        filenames: ['/home/user/project/src/foo.ts'],
        content: '',
        numLines: 0,
      },
      {
        workingDir: '/home/user/project',
      },
    )
    expect(text).toContain('src/foo.ts')
    expect(text).not.toContain('/home/user/project')
  })
})
