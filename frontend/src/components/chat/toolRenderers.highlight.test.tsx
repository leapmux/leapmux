import type { RenderContext } from './messageRenderers'
import { render, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetTokenCache, setCachedTokens, toCachedTokens } from '~/lib/tokenCache'
import { BashHighlightHtml, JsonHighlightHtml } from './toolRenderers'

// The Bash/JSON tool bodies tokenize off-thread via the token worker; mock the client
// so these tests drive the shared useAsyncCodeTokens machinery deterministically.
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn(),
}))

const pausedContext = { syntaxHighlightingPaused: () => true } as unknown as RenderContext

describe('json/bash async token highlighting', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    // The token cache is module-level shared state; reset so a prior test's cached
    // tokens can't satisfy another test's lookup and suppress a dispatch.
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

    render(() => <BashHighlightHtml code="echo hi" />)

    await waitFor(() => expect(tokenizeAsync).toHaveBeenCalledWith('bash', 'echo hi', expect.any(Function)))
  })

  it('serves cached tokens synchronously without dispatching to the worker', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const cached = toCachedTokens([[{ content: '{', htmlStyle: { color: 'rgb(4, 5, 6)' } }]])

    setCachedTokens('json', '{"a":1}', cached)

    const { container } = render(() => <JsonHighlightHtml code={'{"a":1}'} />)

    expect(container.querySelector(`.${cached[0][0].className}`)).not.toBeNull()
    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('does not dispatch oversized JSON (over the char cap) and shows raw text', async () => {
    // The cap is intentional (kept; consistent with the Bash path) -- this documents it.
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const huge = `{"a":"${'x'.repeat(20001)}"}` // > 20000 chars

    const { container } = render(() => <JsonHighlightHtml code={huge} />)

    expect(tokenizeAsync).not.toHaveBeenCalled()
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toContain('xxxxx') // raw JSON text still rendered
  })

  it('does not dispatch empty code (nothing to tokenize)', async () => {
    // Empty bodies are ineligible: tokenizing '' is pointless, so the hook must NOT
    // spawn/round-trip the worker (the size-cap eligibility check treats 0 chars as
    // within its caps, so the guard lives in the hook's currentKey, not in eligible).
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

    const { container } = render(() => <BashHighlightHtml code="echo hi" />)

    await waitFor(() => expect(tokenizeAsync).toHaveBeenCalledWith('bash', 'echo hi', expect.any(Function)))
    await Promise.resolve()
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toContain('echo hi')
  })
})
