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

  it('undefined → "Pending"', () => {
    expect(formatTaskStatus(undefined)).toBe('Pending')
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
  it('all properties present: shows description only when completed', () => {
    const text = renderText({
      childTask: {
        task_id: 'abc',
        task_type: 'shell',
        status: 'completed',
        description: 'Build project',
        output: 'Build succeeded',
        exitCode: 0,
      },
    })
    expect(text).toContain('Build project')
    expect(text).not.toContain('Complete')
  })

  it('missing status: shows "Pending"', () => {
    const text = renderText({
      childTask: {
        description: 'Some task',
      },
    })
    expect(text).toContain('Pending')
  })

  it('missing description: shows status only, no dash separator', () => {
    const text = renderText({
      childTask: {
        status: 'running',
      },
    })
    expect(text).toContain('Running')
    expect(text).not.toContain(' - ')
  })

  it('collapsed with exitCode and task_id: shows summary line', () => {
    const text = renderText({
      childTask: {
        task_id: 'abc',
        status: 'completed',
        description: 'Test run',
        output: 'All tests passed\n42 tests total',
        exitCode: 0,
      },
    })
    expect(text).toContain('Exit code 0')
    expect(text).toContain('Task ID abc')
  })

  it('expanded shows summary (output rendered via thread child)', () => {
    const text = renderText({
      threadExpanded: true,
      childTask: {
        task_id: 'xyz',
        status: 'completed',
        description: 'Run tests',
        exitCode: 0,
      },
    })
    expect(text).toContain('Exit code 0')
    expect(text).toContain('Task ID xyz')
  })

  it('all childTask properties missing (undefined): graceful "Pending" fallback', () => {
    const text = renderText({
      childTask: {},
    })
    expect(text).toContain('Pending')
  })

  it('no childTask at all (no thread child yet): shows "Pending"', () => {
    const text = renderText({})
    expect(text).toContain('Pending')
  })
})
