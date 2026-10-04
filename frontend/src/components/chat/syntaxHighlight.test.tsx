import type { MarkdownRenderContext } from './renderContext'
import { render, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetTokenCache, setCachedTokens, toCachedTokens } from '~/lib/tokenCache'
import { CommandHighlightHtml, JsonHighlightHtml } from './syntaxHighlight'

// Replace the asynchronous token client with a controlled response.
// The tests still exercise the shared token hook.
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn(),
}))

const pausedContext: MarkdownRenderContext = { syntaxHighlightingPaused: () => true }

describe('json/bash async token highlighting', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    // Reset the shared token cache.
    // Tokens from another test must not suppress the required dispatch.
    _resetTokenCache()
  })

  it('dispatches eligible JSON to the worker and renders token spans', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    vi.mocked(tokenizeAsync).mockResolvedValue([[{ content: '{', className: 'sk-json-test' }]])

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} />)

    await waitFor(() => {
      expect(tokenizeAsync).toHaveBeenCalledWith('json', '{"a":1}', expect.any(Function))
      expect(container.querySelector('[data-shiki-token]')).not.toBeNull()
    })
    // The token span carries its shared style class (see shikiStyleClass).
    expect(container.querySelector('.sk-json-test')).not.toBeNull()
  })

  it('dispatches eligible Bash with the bash language', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    vi.mocked(tokenizeAsync).mockResolvedValue([[{ content: 'echo', className: 'sk-bash-test' }]])

    render(() => <CommandHighlightHtml code="echo hi" />)

    await waitFor(() => expect(tokenizeAsync).toHaveBeenCalledWith('bash', 'echo hi', expect.any(Function)))
  })

  it('serves cached tokens synchronously without dispatching to the worker', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const cached = toCachedTokens([[{ content: '{', htmlStyle: { color: 'rgb(4, 5, 6)' } }]])

    setCachedTokens('json', '{"a":1}', cached)

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} />)

    // One token was cached above; `?.` is the type-level guard alone.
    expect(container.querySelector(`.${cached[0]?.[0]?.className ?? ''}`)).not.toBeNull()
    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('does not dispatch oversized JSON (over the char cap) and shows raw text', async () => {
    // JSON and Bash use the same highlight limit.
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const huge = `{"a":"${'x'.repeat(20001)}"}` // > 20000 chars

    const { container } = render(() => <JsonHighlightHtml code={huge} />)

    expect(tokenizeAsync).not.toHaveBeenCalled()
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toContain('xxxxx') // raw JSON text still rendered
  })

  it('does not dispatch empty code (nothing to tokenize)', async () => {
    // Empty text requires no worker dispatch.
    // Size eligibility admits zero characters, so currentKey must refuse that dispatch.
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    const { container } = render(() => <JsonHighlightHtml code="" />)

    expect(tokenizeAsync).not.toHaveBeenCalled()
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
  })

  it('does not dispatch while syntax highlighting is paused', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    render(() => <JsonHighlightHtml code={'{"a":1}'} context={pausedContext} />)

    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('renders raw text when the worker returns null (unknown/failed grammar)', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    vi.mocked(tokenizeAsync).mockResolvedValue(null)

    const { container } = render(() => <CommandHighlightHtml code="echo hi" />)

    await waitFor(() => expect(tokenizeAsync).toHaveBeenCalledWith('bash', 'echo hi', expect.any(Function)))
    await Promise.resolve()
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toContain('echo hi')
  })
})

describe('JsonHighlightHtml output ownership', () => {
  it('marks raw JSON output while token rendering is paused', () => {
    const { container } = render(() => <JsonHighlightHtml dataToolOutputPreview code={'{"count":0}'} context={pausedContext} />)
    expect(container.firstElementChild?.getAttribute('data-tool-output-preview')).toBe('')
    expect(container.textContent).toBe('{"count":0}')
  })

  it('keeps ownership on the same host when tokenized JSON arrives', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    vi.mocked(tokenizeAsync).mockResolvedValue([[{ content: '{"count":0}', className: 'sk-owned-result' }]])
    const { container } = render(() => <JsonHighlightHtml dataToolOutputPreview code={'{"count":0}'} />)
    const host = container.firstElementChild
    await waitFor(() => expect(container.querySelector('[data-shiki-token]')).not.toBeNull())
    expect(container.firstElementChild).toBe(host)
    expect(host?.getAttribute('data-tool-output-preview')).toBe('')
    expect(host?.textContent).toBe('{"count":0}')
  })

  it.each([false, undefined])('leaves ordinary JSON unmarked for ownership %j', (dataToolOutputPreview) => {
    const { container } = render(() => <JsonHighlightHtml {...(dataToolOutputPreview !== undefined ? { dataToolOutputPreview } : {})} code={'{"count":0}'} context={pausedContext} />)
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })

  it('does not mark command input that repeats an output marker', () => {
    const { container } = render(() => <CommandHighlightHtml code="printf native-output-marker" context={pausedContext} />)
    expect(container.textContent).toBe('printf native-output-marker')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
