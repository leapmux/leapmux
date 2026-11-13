import type { MarkdownBodyHtml } from './markdownRendering'
import type { MarkdownRenderContext } from './renderContext'
import { beforeEach, describe, expect, it } from 'vitest'
import { _resetMarkdownCache, getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { holdWorkerReplies, recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { fillMarkdownRenderGate } from '~/test-support/markdownRenderGate'
import { markdownCacheNamespace, markdownHtmlForContext, MAX_ROW_CACHED_MARKDOWN_HTML_CHARS, rowCachedMarkdownHtml } from './markdownRendering'
import { createMessageRenderCacheStore, getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'

function rowContext(rowKey: string, extra: Omit<MarkdownRenderContext, 'renderCache'> = {}): MarkdownRenderContext {
  return {
    renderCache: createMessageRenderCacheStore().forRow(rowKey),
    ...extra,
  }
}

/** A body that holds nothing for its text yet. */
const NOTHING: MarkdownBodyHtml = { displayed: undefined, rowCached: undefined }

/** The HTML of `**x**`, as the Markdown worker renders it. */
const X_HTML = '<p><strong>x</strong></p>'

/** Render `text` in the Markdown worker, so that the shared cache holds its HTML, and return that HTML. */
async function renderedInWorker(text: string): Promise<string | undefined> {
  requestMarkdownHtml(text)
  await settleWorkerReplies()
  return getCachedMarkdownHtml(text)
}

/** The text of each render that reaches the Markdown worker from now on. */
function recordRenderRequests(): () => string[] {
  const requests = recordWorkerRequests('lib/markdownWorker.ts')
  return () => requests.map(request => request.text)
}

/**
 * A body whose HTML is `extra` characters longer than the size limit of the row
 * cache: the worker renders a plain paragraph as `<p>`, the text, and `</p>`.
 */
function paragraphOfHtmlLength(extra: number): string {
  return 'x'.repeat(MAX_ROW_CACHED_MARKDOWN_HTML_CHARS + extra - '<p></p>'.length)
}

describe('rowCachedMarkdownHtml', () => {
  it('returns the HTML that the row holds under the current theme', () => {
    const context = rowContext('row:held')
    setCachedRenderValueForString(context, markdownCacheNamespace(), '**x**', '<p>row</p>')
    expect(rowCachedMarkdownHtml('**x**', context)).toBe('<p>row</p>')
  })

  it('returns undefined for a text that the row does not hold', () => {
    const context = rowContext('row:other')
    setCachedRenderValueForString(context, markdownCacheNamespace(), '**x**', '<p>row</p>')
    expect(rowCachedMarkdownHtml('**y**', context)).toBeUndefined()
  })

  it('ignores an entry stored under another namespace', () => {
    const context = rowContext('row:namespace')
    setCachedRenderValueForString(context, 'markdown-html:another-theme', '**x**', '<p>old theme</p>')
    expect(rowCachedMarkdownHtml('**x**', context)).toBeUndefined()
  })

  it('returns undefined with no render cache', () => {
    expect(rowCachedMarkdownHtml('**x**', undefined)).toBeUndefined()
    expect(rowCachedMarkdownHtml('**x**', {})).toBeUndefined()
  })
})

describe('markdownHtmlForContext', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

  it('returns undefined while the worker renders a body that shows nothing yet', () => {
    const requested = recordRenderRequests()
    expect(markdownHtmlForContext('**x**', rowContext('row:pending'), NOTHING)).toBeUndefined()
    expect(requested()).toEqual(['**x**'])
  })

  it('requests the render in the hidden premeasure pass, where the row completes its Markdown', async () => {
    const requested = recordRenderRequests()
    const context = rowContext('row:premeasure', { premeasureMode: true })

    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBeUndefined()
    expect(requested()).toEqual(['**x**'])
    await settleWorkerReplies()
    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBe(X_HTML)
  })

  it('passes the row offscreen thunk through as the dispatch priority', async () => {
    // The worker gate re-reads this thunk at dequeue time; the render path must hand
    // the SAME function through, not a snapshot of its value. The row is visible
    // when the body asks, and offscreen when the gate decides.
    const gate = fillMarkdownRenderGate()
    let offscreen = false
    markdownHtmlForContext('**x**', rowContext('row:priority', {
      rowOffscreen: () => offscreen,
    }), NOTHING)
    requestMarkdownHtml('A visible body.')
    offscreen = true

    expect(await gate.release()).toEqual(['A visible body.', '**x**'])
  })

  it('stores a completed render in the row cache', async () => {
    const context = rowContext('row:complete')
    markdownHtmlForContext('**x**', context, NOTHING)
    await settleWorkerReplies()

    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBe(X_HTML)

    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBe(X_HTML)
  })

  // The shared cache limits the characters that it holds, and the row cache keeps
  // a row's entries for as long as the row stays among the recent ones. A large
  // body in the row cache would outlive the shared cache's eviction and defeat its
  // limit, so only the shared cache holds it.
  it('keeps a render past the size limit out of the row cache, and still shows it', async () => {
    const context = rowContext('row:large')
    const text = paragraphOfHtmlLength(1)
    const large = await renderedInWorker(text)
    expect(large).toHaveLength(MAX_ROW_CACHED_MARKDOWN_HTML_CHARS + 1)

    expect(markdownHtmlForContext(text, context, NOTHING)).toBe(large)
    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), text)).toBeUndefined()
  })

  it('stores a render of exactly the size limit in the row cache', async () => {
    const context = rowContext('row:at-limit')
    const text = paragraphOfHtmlLength(0)
    const atLimit = await renderedInWorker(text)
    expect(atLimit).toHaveLength(MAX_ROW_CACHED_MARKDOWN_HTML_CHARS)

    markdownHtmlForContext(text, context, NOTHING)

    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), text)).toBe(atLimit)
  })

  it('caches nothing for a pending body', async () => {
    const hold = holdWorkerReplies('lib/markdownWorker.ts')
    const context = rowContext('row:nothing')
    markdownHtmlForContext('**x**', context, NOTHING)
    await settleWorkerReplies()

    expect(hold.pending()).toBe(1)
    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBeUndefined()
  })

  it('serves the row cache without a request', () => {
    const requested = recordRenderRequests()
    expect(markdownHtmlForContext('**x**', rowContext('row:cached'), {
      displayed: undefined,
      rowCached: '<p>row</p>',
    })).toBe('<p>row</p>')
    expect(requested()).toEqual([])
  })

  it('serves the row cache in place of the old-theme HTML that the body displays', () => {
    const requested = recordRenderRequests()
    expect(markdownHtmlForContext('**x**', rowContext('row:cached-theme'), {
      displayed: '<p>old theme</p>',
      rowCached: '<p>row</p>',
    })).toBe('<p>row</p>')
    expect(requested()).toEqual([])
  })

  it('serves the shared cache into the row cache without a request', async () => {
    const context = rowContext('row:shared')
    await renderedInWorker('**x**')
    const requested = recordRenderRequests()

    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBe(X_HTML)
    expect(getCachedRenderValueForString(context, markdownCacheNamespace(), '**x**')).toBe(X_HTML)
    expect(requested()).toEqual([])
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
  ] as const)('keeps the displayed HTML while %s, even when a newer render exists', async (_label, flags) => {
    await renderedInWorker('**x**')
    const requested = recordRenderRequests()

    expect(markdownHtmlForContext('**x**', rowContext('row:stable', flags), {
      displayed: '<p>displayed</p>',
      rowCached: '<p>row</p>',
    })).toBe('<p>displayed</p>')
    expect(requested()).toEqual([])
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
  ] as const)('still requests a body that shows nothing yet while %s', async (_label, flags) => {
    const requested = recordRenderRequests()
    const context = rowContext('row:first', flags)

    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBeUndefined()
    expect(requested()).toEqual(['**x**'])
    await settleWorkerReplies()
    expect(markdownHtmlForContext('**x**', context, NOTHING)).toBe(X_HTML)
  })

  it('keeps the old-theme HTML while the new theme renders, instead of turning pending', () => {
    // A theme change empties the caches and moves the namespace, so the body has
    // no current-theme HTML. What it displays stays until the new render lands.
    const requested = recordRenderRequests()
    expect(markdownHtmlForContext('**x**', rowContext('row:theme'), {
      displayed: '<p>old theme</p>',
      rowCached: undefined,
    })).toBe('<p>old theme</p>')
    expect(requested()).toEqual(['**x**'])
  })

  it('switches to the new render as soon as it lands', async () => {
    const context = rowContext('row:theme-landed')
    const body: MarkdownBodyHtml = { displayed: '<p>old theme</p>', rowCached: undefined }
    expect(markdownHtmlForContext('**x**', context, body)).toBe('<p>old theme</p>')
    await settleWorkerReplies()

    expect(markdownHtmlForContext('**x**', context, body)).toBe(X_HTML)
  })

  it('returns undefined with no render context and no render', () => {
    const requested = recordRenderRequests()
    expect(markdownHtmlForContext('**x**', undefined, NOTHING)).toBeUndefined()
    expect(requested()).toEqual(['**x**'])
  })

  it('serves the newer render once the pause clears', async () => {
    let paused = true
    const context = rowContext('row:pause', {
      syntaxHighlightingPaused: () => paused,
    })
    await renderedInWorker('**x**')
    const body: MarkdownBodyHtml = { displayed: '<p>displayed</p>', rowCached: undefined }

    expect(markdownHtmlForContext('**x**', context, body)).toBe('<p>displayed</p>')
    paused = false
    expect(markdownHtmlForContext('**x**', context, body)).toBe(X_HTML)
  })
})
