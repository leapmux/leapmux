import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

// Mock renderAnsi to avoid shiki initialization in tests.
vi.mock('~/lib/renderAnsi', () => ({
  // eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
  containsAnsi: (text: string) => /\x1B\[[\d;]*m/.test(text),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

// Mock renderMarkdown to avoid shiki initialization in tests.
vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => text,
}))

const { formatGrepSummary } = await import('./rendererUtils')
const { renderMessageContent } = await import('./messageRenderers')
type RenderContext = import('./messageRenderers').RenderContext

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
function renderToolUseText(context?: RenderContext): string {
  const msg = makeGrepToolUse({ path: '/home/user/project/src' })
  const result = renderMessageContent(msg, 2 /* ASSISTANT */, context)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a Grep tool_result message and return its text content. */
function renderToolResultText(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): string {
  const msg = makeGrepToolResult(resultContent, toolUseResult)
  const result = renderMessageContent(msg, 1 /* USER */, context)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a Grep tool_result message and return its container element. */
function renderToolResultContainer(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): HTMLElement {
  const msg = makeGrepToolResult(resultContent, toolUseResult)
  const result = renderMessageContent(msg, 1 /* USER */, context)
  const { container } = render(() => result)
  return container
}

describe('formatGrepSummary', () => {
  it('returns null when no structured data and no fallback', () => {
    expect(formatGrepSummary(undefined, undefined)).toBeNull()
  })

  it('returns fallback when no structured data', () => {
    expect(formatGrepSummary(undefined, undefined, 'Custom fallback')).toBe('Custom fallback')
  })

  it('returns "No matches found" when both are 0', () => {
    expect(formatGrepSummary(0, 0)).toBe('No matches found')
  })

  it('returns fallback when both are 0 and fallback provided', () => {
    expect(formatGrepSummary(0, 0, 'No files found')).toBe('No files found')
  })

  it('returns "Found N files" for files only', () => {
    expect(formatGrepSummary(5, 0)).toBe('Found 5 files')
  })

  it('returns "Found 1 file" for singular file', () => {
    expect(formatGrepSummary(1, 0)).toBe('Found 1 file')
  })

  it('returns "Found N lines" for lines only', () => {
    expect(formatGrepSummary(0, 7)).toBe('Found 7 lines')
  })

  it('returns "Found 1 line" for singular line', () => {
    expect(formatGrepSummary(0, 1)).toBe('Found 1 line')
  })

  it('returns "Found N files and M lines" for both', () => {
    expect(formatGrepSummary(3, 12)).toBe('Found 3 files and 12 lines')
  })

  it('returns "Found 1 file and 1 line" for singular both', () => {
    expect(formatGrepSummary(1, 1)).toBe('Found 1 file and 1 line')
  })
})

describe('grep tool_use collapsed summary', () => {
  it('shows pattern in header', () => {
    const text = renderToolUseText()
    expect(text).toContain('"ToolHeaderActions"')
  })

  it('shows path and "Found N lines" summary', () => {
    const text = renderToolUseText({
      childGrepNumFiles: 0,
      childGrepNumLines: 7,
      childResultContent: 'some match content',
    })
    expect(text).toContain('src')
    expect(text).toContain('Found 7 lines')
  })

  it('shows "No matches found" when no results', () => {
    const text = renderToolUseText({
      childGrepNumFiles: 0,
      childGrepNumLines: 0,
      childResultContent: 'No matches found',
    })
    expect(text).toContain('No matches found')
  })

  it('shows fallback from childResultContent when numFiles and numLines are 0', () => {
    const text = renderToolUseText({
      childGrepNumFiles: 0,
      childGrepNumLines: 0,
      childResultContent: 'No files found',
    })
    expect(text).toContain('No files found')
  })

  it('shows "Found N files" for files_with_matches mode', () => {
    const text = renderToolUseText({
      childGrepNumFiles: 3,
      childGrepNumLines: 0,
    })
    expect(text).toContain('Found 3 files')
  })

  it('shows "Found N files and M lines" for both', () => {
    const text = renderToolUseText({
      childGrepNumFiles: 2,
      childGrepNumLines: 10,
    })
    expect(text).toContain('Found 2 files and 10 lines')
  })

  it('shows only path when no result data yet', () => {
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

  it('shows fallback content when numFiles and numLines are 0 with custom message', () => {
    const text = renderToolResultText('No files found', {
      tool_name: 'Grep',
      numFiles: 0,
      filenames: [],
      content: '',
      numLines: 0,
    })
    expect(text).toContain('No files found')
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
    const container = renderToolResultContainer(
      'src/foo.ts\nsrc/bar.ts',
      {
        tool_name: 'Grep',
        numFiles: 2,
        filenames: ['src/foo.ts', 'src/bar.ts'],
        content: '',
        numLines: 0,
      },
    )
    const listItems = container.querySelectorAll('li')
    expect(listItems.length).toBe(2)
    expect(listItems[0].textContent).toContain('foo.ts')
    expect(listItems[1].textContent).toContain('bar.ts')
  })

  it('shows both file list and content when both numFiles and numLines > 0', () => {
    const matchContent = '42:const x = 1;'
    const container = renderToolResultContainer(
      'src/foo.ts\n42:const x = 1;',
      {
        tool_name: 'Grep',
        numFiles: 1,
        filenames: ['src/foo.ts'],
        content: matchContent,
        numLines: 1,
      },
    )
    const text = container.textContent ?? ''
    // Check for file list
    const listItems = container.querySelectorAll('li')
    expect(listItems.length).toBe(1)
    // Check for content
    expect(text).toContain('const x = 1;')
  })

  it('falls back to raw preformatted text when tool_use_result is missing', () => {
    const text = renderToolResultText('raw grep output line 1\nline 2', undefined, {
      parentToolName: 'Grep',
    })
    expect(text).toContain('raw grep output line 1')
    expect(text).toContain('line 2')
  })

  it('relativizes file paths in file list', () => {
    const container = renderToolResultContainer(
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
    const listItems = container.querySelectorAll('li')
    expect(listItems.length).toBe(1)
    expect(listItems[0].textContent).toBe('src/foo.ts')
  })
})
