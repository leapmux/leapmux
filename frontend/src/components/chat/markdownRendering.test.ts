import type { MarkdownRenderContext } from './renderContext'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { markdownCacheNamespace, markdownHtmlForContext } from './markdownRendering'
import { createMessageRenderCacheStore, getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'

vi.mock('~/lib/renderMarkdown', () => ({
  getCachedMarkdownHtml: vi.fn(() => undefined),
  requestMarkdownHtml: vi.fn(() => undefined),
}))

const requestMock = vi.mocked(requestMarkdownHtml)
const sharedCacheMock = vi.mocked(getCachedMarkdownHtml)

function rowContext(rowKey: string, extra: Omit<MarkdownRenderContext, 'renderCache'> = {}): MarkdownRenderContext {
  return {
    renderCache: createMessageRenderCacheStore().forRow(rowKey),
    ...extra,
  }
}

describe('markdownHtmlForContext', () => {
  beforeEach(() => {
    requestMock.mockReset()
    requestMock.mockReturnValue(undefined)
    sharedCacheMock.mockReset()
    sharedCacheMock.mockReturnValue(undefined)
  })

  it('returns undefined while the worker renders a body that shows nothing yet', () => {
    expect(markdownHtmlForContext('**x**', rowContext('row:pending'), undefined)).toBeUndefined()
    expect(requestMock).toHaveBeenCalledWith('**x**', undefined)
  })

  it('requests the render in the hidden premeasure pass, where the row completes its Markdown', () => {
    requestMock.mockReturnValue('<p>final</p>')
    expect(markdownHtmlForContext('**x**', rowContext('row:premeasure', { premeasureMode: true }), undefined)).toBe('<p>final</p>')
    expect(requestMock).toHaveBeenCalledWith('**x**', undefined)
  })

  it('passes the row offscreen thunk through as the dispatch priority', () => {
    // The worker gate re-reads this thunk at dequeue time; the render path must hand
    // the SAME function through, not a snapshot of its value.
    const rowOffscreen = () => true
    markdownHtmlForContext('**x**', rowContext('row:priority', { rowOffscreen }), undefined)
    expect(requestMock).toHaveBeenCalledWith('**x**', rowOffscreen)
  })

  it('stores a completed render in the row cache', () => {
    const context = rowContext('row:complete')
    requestMock.mockReturnValue('<p>final</p>')

    expect(markdownHtmlForContext('**x**', context, undefined)).toBe('<p>final</p>')

    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBe('<p>final</p>')
  })

  it('caches nothing for a pending body', () => {
    const context = rowContext('row:nothing')
    markdownHtmlForContext('**x**', context, undefined)
    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBeUndefined()
  })

  it('serves the row cache without a request', () => {
    const context = rowContext('row:cached')
    setCachedRenderValueForString(context, markdownCacheNamespace(), '**x**', '<p>row</p>')

    expect(markdownHtmlForContext('**x**', context, undefined)).toBe('<p>row</p>')
    expect(requestMock).not.toHaveBeenCalled()
    expect(sharedCacheMock).not.toHaveBeenCalled()
  })

  it('serves the shared cache into the row cache without a request', () => {
    const context = rowContext('row:shared')
    sharedCacheMock.mockReturnValue('<p>shared</p>')

    expect(markdownHtmlForContext('**x**', context, undefined)).toBe('<p>shared</p>')
    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBe('<p>shared</p>')
    expect(requestMock).not.toHaveBeenCalled()
  })

  it.each([
    [
      'text is selected',
      {
        textSelectionActive: () => true,
      },
    ],
    [
      'the view scrolls',
      {
        syntaxHighlightingPaused: () => true,
      },
    ],
  ] as const)('keeps the displayed HTML while %s, even when a newer render exists', (_label, flags) => {
    sharedCacheMock.mockReturnValue('<p>newer</p>')

    expect(markdownHtmlForContext('**x**', rowContext('row:stable', flags), '<p>displayed</p>')).toBe('<p>displayed</p>')
    expect(requestMock).not.toHaveBeenCalled()
  })

  it.each([
    [
      'text is selected',
      {
        textSelectionActive: () => true,
      },
    ],
    [
      'the view scrolls',
      {
        syntaxHighlightingPaused: () => true,
      },
    ],
  ] as const)('still requests a body that shows nothing yet while %s', (_label, flags) => {
    requestMock.mockReturnValue('<p>final</p>')
    expect(markdownHtmlForContext('**x**', rowContext('row:first', flags), undefined)).toBe('<p>final</p>')
    expect(requestMock).toHaveBeenCalledTimes(1)
  })

  it('keeps the old-theme HTML while the new theme renders, instead of turning pending', () => {
    // A theme change empties the caches and moves the namespace, so the body has
    // no current-theme HTML. What it displays stays until the new render lands.
    expect(markdownHtmlForContext('**x**', rowContext('row:theme'), '<p>old theme</p>')).toBe('<p>old theme</p>')
    expect(requestMock).toHaveBeenCalledWith('**x**', undefined)
  })

  it('switches to the new render as soon as it lands', () => {
    requestMock.mockReturnValue('<p>new theme</p>')
    expect(markdownHtmlForContext('**x**', rowContext('row:theme-landed'), '<p>old theme</p>')).toBe('<p>new theme</p>')
  })

  it('returns undefined with no render context and no render', () => {
    expect(markdownHtmlForContext('**x**', undefined, undefined)).toBeUndefined()
    expect(requestMock).toHaveBeenCalledWith('**x**', undefined)
  })

  it('serves the newer render once the pause clears', () => {
    let paused = true
    const context = rowContext('row:pause', {
      syntaxHighlightingPaused: () => paused,
    })
    sharedCacheMock.mockReturnValue('<p>newer</p>')

    expect(markdownHtmlForContext('**x**', context, '<p>displayed</p>')).toBe('<p>displayed</p>')
    paused = false
    expect(markdownHtmlForContext('**x**', context, '<p>displayed</p>')).toBe('<p>newer</p>')
  })
})
