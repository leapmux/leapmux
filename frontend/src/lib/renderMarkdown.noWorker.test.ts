import { afterEach, describe, expect, it, vi } from 'vitest'
import { settle } from '~/test-support/async'
import { MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE } from './markdownSourceFallback'

// ---------------------------------------------------------------------------
// renderMarkdown where no Worker exists
//
// A file of its own: the clients spawn their workers once per module instance,
// so the test imports them afresh after it removes the Worker, and a fresh module
// registry must not reach the tests that share the worker of the jsdom setup.
// ---------------------------------------------------------------------------

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('requestMarkdownHtml', () => {
  it('shows the escaped source where no Worker exists, as for a worker that cannot start', async () => {
    vi.stubGlobal('Worker', undefined)
    vi.resetModules()
    const { requestMarkdownHtml } = await import('./renderMarkdown')
    const text = 'a **bold** body'

    // The main thread never parses Markdown, even where no worker can.
    expect(requestMarkdownHtml(text)).toBeUndefined()
    await settle()

    const html = requestMarkdownHtml(text)
    expect(html).toContain(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)
    expect(html).toContain('a **bold** body')
    expect(html).not.toContain('<strong>')
  })
})
