import type { MarkdownRenderRequest } from './markdownWorker'
import type { CapturingWorker } from '~/test-support/fakeWorker'
import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installCapturingWorkers } from '~/test-support/fakeWorker'
import { _resetArtifactStoreForTest, getArtifact, putArtifact } from './renderArtifactStore'
import { _resetMarkdownCache, markdownArtifactNs, requestMarkdownHtml } from './renderMarkdown'
import { _resetShikiStyleClassesForTest } from './shikiStyleClass'
import { readInjectedShikiRules } from './shikiStyleClass.testkit'

// ---------------------------------------------------------------------------
// renderMarkdown with a Markdown worker that the test answers by hand
//
// Each test replaces the answering stand-in Worker with one that records each
// render, so that the test can state the reply: a dictionary of token styles,
// or one that lacks a declaration.
// ---------------------------------------------------------------------------

/**
 * The workers that each test's stub recorded. The client keeps the first worker
 * that it spawns for the whole file, so a test can post to a worker that the
 * stub of an earlier test recorded.
 */
const spawnedByTest: CapturingWorker<MarkdownRenderRequest>[][] = []

function dispatchesFor(text: string): Array<{ worker: CapturingWorker<MarkdownRenderRequest>, id: number }> {
  return spawnedByTest.flat().flatMap(worker => worker.messages
    .filter(m => m.text === text)
    .map(m => ({ worker, id: m.id })))
}

/** Answer the one worker dispatch of `text` with `reply`. */
async function replyTo(text: string, reply: { html: string, styles: Record<string, string> }): Promise<void> {
  await vi.waitFor(() => expect(dispatchesFor(text)).toHaveLength(1))
  const dispatch = dispatchesFor(text)[0]
  if (dispatch === undefined)
    throw new Error('expected a worker dispatch')
  dispatch.worker.reply({ id: dispatch.id, retryable: false, ...reply })
}

describe('renderMarkdown shared token-style classes', () => {
  beforeEach(() => {
    _resetMarkdownCache()
    _resetShikiStyleClassesForTest()
    spawnedByTest.push(installCapturingWorkers<MarkdownRenderRequest>())
    vi.stubGlobal('indexedDB', new IDBFactory())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    _resetArtifactStoreForTest()
  })

  it('serves a persisted {html, styles} render artifact and injects its rules without dispatching the worker', async () => {
    const text = 'persisted warm start body'
    await putArtifact(markdownArtifactNs(), text, {
      h: '<p data-persisted="">persisted warm start body</p>',
      s: { 'sk-00000000-1s': '--shiki-light:#123;--shiki-dark:#456' },
    })

    // The first call has nothing to show: it starts the async warm-start and
    // reports the body pending. It never parses the body on this thread.
    expect(requestMarkdownHtml(text)).toBeUndefined()

    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<p data-persisted="">persisted warm start body</p>')
    })
    // The dictionary's rules were injected before the HTML could render.
    expect(readInjectedShikiRules()).toContain('.sk-00000000-1s{--shiki-light:#123;--shiki-dark:#456}')
    // The warm-start never dispatched this text to a worker.
    expect(dispatchesFor(text)).toHaveLength(0)
  })

  it('rejects a legacy plain-string render artifact and falls through to the worker', async () => {
    const text = 'legacy artifact body'
    // The pre-{h,s} artifact shape: a bare HTML string.
    await putArtifact(markdownArtifactNs(), text, '<p>legacy</p>')

    requestMarkdownHtml(text)

    // The malformed hit is a miss: the render dispatches to the worker.
    await replyTo(text, { html: '<p>legacy fallback</p>', styles: {} })
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<p>legacy fallback</p>')
    })
  })

  it('rejects an oversized persisted html render artifact and falls through to the worker', async () => {
    const text = 'oversized artifact body'
    await putArtifact(markdownArtifactNs(), text, {
      h: `<p>${'x'.repeat(512 * 1024)}</p>`,
      s: {},
    })

    requestMarkdownHtml(text)

    await replyTo(text, { html: '<p>oversized fallback</p>', styles: {} })
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe('<p>oversized fallback</p>')
    })
  })

  it('injects a worker result\'s style dictionary and persists {html, styles}', async () => {
    const text = 'worker highlighted body'

    requestMarkdownHtml(text)
    await replyTo(text, {
      html: '<pre class="shiki"><code><span class="sk-00000001-13">x</span></code></pre>',
      styles: { 'sk-00000001-13': '--shiki-light:#aaa' },
    })

    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toContain('sk-00000001-13')
    })
    // The worker has no document; the main thread injected the shipped rules.
    expect(readInjectedShikiRules()).toContain('.sk-00000001-13{--shiki-light:#aaa}')
    // Persisted for the next session's warm start, dictionary included.
    await vi.waitFor(async () => {
      await expect(getArtifact(markdownArtifactNs(), text)).resolves.toEqual({
        h: '<pre class="shiki"><code><span class="sk-00000001-13">x</span></code></pre>',
        s: { 'sk-00000001-13': '--shiki-light:#aaa' },
      })
    })
  })

  // The worker sends each declaration once, with the first render that uses it. A
  // later render's persisted copy must still carry it: after a reload, the later
  // body can show first, and nothing else then injects the rule.
  it('persists the declarations of every class a render uses, also those an earlier reply shipped', async () => {
    const first = 'first body with a shared token style'
    const second = 'second body with a shared token style'
    const html = '<pre class="shiki"><code><span class="sk-0000000a-13">x</span></code></pre>'

    requestMarkdownHtml(first)
    requestMarkdownHtml(second)
    await replyTo(first, {
      html,
      styles: { 'sk-0000000a-13': '--shiki-light:#aaa' },
    })
    await replyTo(second, { html, styles: {} })

    await vi.waitFor(async () => {
      await expect(getArtifact(markdownArtifactNs(), second)).resolves.toEqual({
        h: html,
        s: { 'sk-0000000a-13': '--shiki-light:#aaa' },
      })
    })
  })

  it('persists no render that uses a class whose declaration this thread does not know', async () => {
    const text = 'body with an unknown token style'
    const html = '<pre class="shiki"><code><span class="sk-0000000b-13">x</span></code></pre>'

    requestMarkdownHtml(text)
    await replyTo(text, { html, styles: {} })

    // The render shows; only its persisted copy is withheld, because a reload could
    // not colour its tokens.
    await vi.waitFor(() => {
      expect(requestMarkdownHtml(text)).toBe(html)
    })
    await expect(getArtifact(markdownArtifactNs(), text)).resolves.toBeUndefined()
  })
})
