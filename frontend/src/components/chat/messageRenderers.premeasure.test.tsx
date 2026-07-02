import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getCachedMarkdownHtml, renderMarkdown, renderMarkdownCachedOrPlain, renderMarkdownPlain } from '~/lib/renderMarkdown'
import { createMessageRenderCacheStore, getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'
import { MarkdownText } from './messageRenderers'

vi.mock('~/lib/renderMarkdown', () => ({
  getCachedMarkdownHtml: vi.fn(() => undefined),
  renderMarkdown: vi.fn(() => '<p>highlighted</p>'),
  renderMarkdownCachedOrPlain: vi.fn(() => '<p>cached-or-plain</p>'),
  renderMarkdownPlain: vi.fn(() => '<p>plain</p>'),
}))

describe('markdown text premeasure mode', () => {
  beforeEach(() => {
    vi.mocked(getCachedMarkdownHtml).mockReset()
    vi.mocked(getCachedMarkdownHtml).mockReturnValue(undefined)
    vi.mocked(renderMarkdown).mockClear()
    vi.mocked(renderMarkdownCachedOrPlain).mockClear()
    vi.mocked(renderMarkdownPlain).mockClear()
  })

  it('uses plain markdown without dispatching highlighted rendering during premeasure', () => {
    const { container } = render(() => <MarkdownText text={'```ts\nx\n```'} context={{ premeasureMode: true }} />)

    expect(container.innerHTML).toContain('<p>plain</p>')
    expect(renderMarkdownPlain).toHaveBeenCalledWith('```ts\nx\n```')
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('uses stable plain markdown without subscribing to cached-highlight updates while text is selected', () => {
    const { container } = render(() => <MarkdownText text={'```ts\nx\n```'} context={{ textSelectionActive: () => true }} />)

    expect(container.innerHTML).toContain('<p>plain</p>')
    expect(getCachedMarkdownHtml).toHaveBeenCalledWith('```ts\nx\n```')
    expect(renderMarkdownPlain).toHaveBeenCalledWith('```ts\nx\n```')
    expect(renderMarkdownCachedOrPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('keeps row-cached highlighted markdown while text is selected', () => {
    const cache = createMessageRenderCacheStore().forRow('row:selection')
    setCachedRenderValueForString({ renderCache: cache }, 'markdown-html', '**x**', '<p>row-highlighted</p>')

    const { container } = render(() => <MarkdownText text="**x**" context={{ renderCache: cache, textSelectionActive: () => true }} />)

    expect(container.innerHTML).toContain('<p>row-highlighted</p>')
    expect(getCachedMarkdownHtml).not.toHaveBeenCalled()
    expect(renderMarkdownCachedOrPlain).not.toHaveBeenCalled()
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('keeps the exact displayed markdown when selection becomes active mid-drag', () => {
    const cache = createMessageRenderCacheStore().forRow('row:displayed-selection')
    const [selectionActive, setSelectionActive] = createSignal(false)
    const { container } = render(() => (
      <MarkdownText
        text="**x**"
        context={{ renderCache: cache, textSelectionActive: selectionActive }}
      />
    ))

    expect(container.innerHTML).toContain('<p>highlighted</p>')
    vi.mocked(getCachedMarkdownHtml).mockClear()
    vi.mocked(renderMarkdown).mockClear()
    vi.mocked(renderMarkdownPlain).mockClear()

    setSelectionActive(true)

    expect(container.innerHTML).toContain('<p>highlighted</p>')
    expect(getCachedMarkdownHtml).not.toHaveBeenCalled()
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('passes the context rowOffscreen thunk through as the dispatch priority', () => {
    // The worker gate re-reads this thunk at dequeue time; the render path
    // must hand the SAME function through, not a snapshot of its value.
    const rowOffscreen = () => true
    render(() => <MarkdownText text="**x**" context={{ rowOffscreen }} />)

    expect(renderMarkdown).toHaveBeenCalledWith('**x**', false, rowOffscreen)
  })

  it('renders highlighted markdown after an initially active text selection clears', () => {
    const [selectionActive, setSelectionActive] = createSignal(true)
    const { container } = render(() => (
      <MarkdownText
        text="**x**"
        context={{ textSelectionActive: selectionActive }}
      />
    ))

    expect(container.innerHTML).toContain('<p>plain</p>')
    expect(renderMarkdown).not.toHaveBeenCalled()

    setSelectionActive(false)

    expect(container.innerHTML).toContain('<p>highlighted</p>')
    expect(renderMarkdown).toHaveBeenCalledWith('**x**', false, undefined)
  })

  it('uses cached highlighted markdown when available and does not dispatch highlighted rendering while visible scrolling is busy', () => {
    const { container } = render(() => <MarkdownText text={'```ts\nx\n```'} context={{ syntaxHighlightingPaused: () => true }} />)

    expect(container.innerHTML).toContain('<p>cached-or-plain</p>')
    expect(renderMarkdownCachedOrPlain).toHaveBeenCalledWith('```ts\nx\n```')
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('stores shared highlighted markdown in the row cache while visible scrolling is busy', () => {
    const cache = createMessageRenderCacheStore().forRow('row:paused-cache')
    vi.mocked(getCachedMarkdownHtml).mockReturnValue('<p>shared-highlighted</p>')

    const { container } = render(() => <MarkdownText text="**x**" context={{ renderCache: cache, syntaxHighlightingPaused: () => true }} />)

    expect(container.innerHTML).toContain('<p>shared-highlighted</p>')
    expect(getCachedRenderValueForString({ renderCache: cache }, 'markdown-html', '**x**')).toBe('<p>shared-highlighted</p>')
    expect(renderMarkdownCachedOrPlain).not.toHaveBeenCalled()
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('keeps row-cached highlighted markdown while visible scrolling is busy', () => {
    const cache = createMessageRenderCacheStore().forRow('row:1')
    setCachedRenderValueForString({ renderCache: cache }, 'markdown-html', '**x**', '<p>row-highlighted</p>')

    const { container } = render(() => <MarkdownText text="**x**" context={{ renderCache: cache, syntaxHighlightingPaused: () => true }} />)

    expect(container.innerHTML).toContain('<p>row-highlighted</p>')
    expect(renderMarkdownCachedOrPlain).not.toHaveBeenCalled()
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it('uses highlighted markdown outside premeasure', () => {
    const { container } = render(() => <MarkdownText text="**x**" />)

    expect(container.innerHTML).toContain('<p>highlighted</p>')
    expect(renderMarkdown).toHaveBeenCalledWith('**x**', false, undefined)
    expect(renderMarkdownPlain).not.toHaveBeenCalled()
  })

  it('does not row-cache the first active render unless highlighted markdown has completed', () => {
    const cache = createMessageRenderCacheStore().forRow('row:active-placeholder')

    render(() => <MarkdownText text="**x**" context={{ renderCache: cache }} />)

    expect(renderMarkdown).toHaveBeenCalledWith('**x**', false, undefined)
    expect(getCachedRenderValueForString({ renderCache: cache }, 'markdown-html', '**x**')).toBeUndefined()
  })

  it('row-caches completed highlighted markdown during active rendering', () => {
    const cache = createMessageRenderCacheStore().forRow('row:active-highlighted')
    vi.mocked(getCachedMarkdownHtml).mockReturnValue('<p>shared-highlighted</p>')

    const { container } = render(() => <MarkdownText text="**x**" context={{ renderCache: cache }} />)

    expect(container.innerHTML).toContain('<p>shared-highlighted</p>')
    expect(getCachedRenderValueForString({ renderCache: cache }, 'markdown-html', '**x**')).toBe('<p>shared-highlighted</p>')
  })
})
