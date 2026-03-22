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

const { formatTaskStatus, firstNonEmptyLine, renderMessageContent } = await import('./messageRenderers')
type RenderContext = import('./messageRenderers').RenderContext

/** Construct a TaskOutput tool_use assistant message object. */
function makeTaskOutputMessage() {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test',
        name: 'TaskOutput',
        input: { task_id: 'test123', block: true, timeout: 120000 },
      }],
    },
  }
}

/** Render a TaskOutput message with the given context and return the text content. */
function renderText(context?: RenderContext): string {
  const msg = makeTaskOutputMessage()
  const result = renderMessageContent(msg, 2 /* ASSISTANT */, context)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

describe('formatTaskStatus', () => {
  it('"completed" → "Complete"', () => {
    expect(formatTaskStatus('completed')).toBe('Complete')
  })

  it('"failed" → "Failed"', () => {
    expect(formatTaskStatus('failed')).toBe('Failed')
  })

  it('"running" → "Running"', () => {
    expect(formatTaskStatus('running')).toBe('Running')
  })

  it('undefined → "Waiting for output"', () => {
    expect(formatTaskStatus(undefined)).toBe('Waiting for output')
  })
})

describe('firstNonEmptyLine', () => {
  it('multi-line text with leading blank lines → returns first non-empty trimmed line', () => {
    expect(firstNonEmptyLine('\n\n  hello world  \nsecond line')).toBe('hello world')
  })

  it('empty string → returns null', () => {
    expect(firstNonEmptyLine('')).toBeNull()
  })

  it('undefined → returns null', () => {
    expect(firstNonEmptyLine(undefined)).toBeNull()
  })
})

describe('renderTaskOutput', () => {
  it('always shows "Waiting for output" (child data fields removed)', () => {
    const text = renderText()
    expect(text).toContain('Waiting for output')
  })

  it('shows "Waiting for output" even with empty context', () => {
    const text = renderText({})
    expect(text).toContain('Waiting for output')
  })

  it('does not show task status or exit code (no child data)', () => {
    const text = renderText({})
    expect(text).not.toContain('Running')
    expect(text).not.toContain('Complete')
    expect(text).not.toContain('Exit code')
    expect(text).not.toContain('Task ID')
  })
})
