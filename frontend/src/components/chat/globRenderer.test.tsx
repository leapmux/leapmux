import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

// eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
const ANSI_ESCAPE_RE = /\x1B\[[\d;]*m/

// Mock renderAnsi to avoid shiki initialization in tests.
vi.mock('~/lib/renderAnsi', () => ({
  containsAnsi: (text: string) => ANSI_ESCAPE_RE.test(text),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

// Mock renderMarkdown to avoid shiki initialization in tests.
vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => text,
}))

const { formatGlobSummary } = await import('./rendererUtils')
const { renderMessageContent } = await import('./messageRenderers')
type RenderContext = import('./messageRenderers').RenderContext

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
  const result = renderMessageContent(msg, 2 /* ASSISTANT */, context)
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
  const result = renderMessageContent(msg, 1 /* USER */, context)
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

describe('formatGlobSummary', () => {
  it('returns null when numFiles is undefined and no fallback', () => {
    expect(formatGlobSummary(undefined)).toBeNull()
  })

  it('returns fallback when numFiles is undefined', () => {
    expect(formatGlobSummary(undefined, undefined, undefined, 'Custom fallback')).toBe('Custom fallback')
  })

  it('returns "No files found" when numFiles is 0', () => {
    expect(formatGlobSummary(0)).toBe('No files found')
  })

  it('returns fallback when numFiles is 0 and fallback provided', () => {
    expect(formatGlobSummary(0, undefined, undefined, 'No matching files')).toBe('No matching files')
  })

  it('returns "Found N files" for numFiles > 0', () => {
    expect(formatGlobSummary(4)).toBe('Found 4 files')
  })

  it('returns "Found 1 file" for singular', () => {
    expect(formatGlobSummary(1)).toBe('Found 1 file')
  })

  it('includes duration when durationMs is provided', () => {
    expect(formatGlobSummary(4, 1011)).toBe('Found 4 files \u00B7 Took 1s')
  })

  it('includes "Result truncated" when truncated is true', () => {
    expect(formatGlobSummary(100, undefined, true)).toBe('Found 100 files \u00B7 Result truncated')
  })

  it('includes both duration and truncated', () => {
    expect(formatGlobSummary(50, 2500, true)).toBe('Found 50 files \u00B7 Took 3s \u00B7 Result truncated')
  })

  it('omits truncated when false', () => {
    expect(formatGlobSummary(4, 1011, false)).toBe('Found 4 files \u00B7 Took 1s')
  })

  it('shows duration with "No files found"', () => {
    expect(formatGlobSummary(0, 500)).toBe('No files found \u00B7 Took 1s')
  })
})

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
      { parentToolName: 'Glob' },
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
