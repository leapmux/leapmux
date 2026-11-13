import type { RenderReadinessScope } from './renderReadiness'
import { render } from '@solidjs/testing-library'
import { createRoot, createSignal, Show } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetMarkdownCache, getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { shikiHighlighter } from '~/lib/shikiSyncHighlighter'
import { syntaxPairFor } from '~/lib/syntaxThemes'
import { setSyntaxTheme } from '~/lib/syntaxThemeStore'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { holdWorkerReplies, recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { fillMarkdownRenderGate } from '~/test-support/markdownRenderGate'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { markdownCacheNamespace } from './markdownRendering'
import { MarkdownPrefetch, MarkdownText } from './MarkdownText'
import { createMessageRenderCacheStore, getCachedRenderValueForString } from './messageRenderCache'
import { createRenderReadinessScope } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX } from './widgets/ChatRowSkeleton'

// Every body renders in the real Markdown worker, which the stand-in Worker runs
// (see ~/test-support/fakeWorker). A test that needs a body to wait holds the
// replies of that worker.

/** A body whose HTML depends on the syntax theme: Shiki colours its code. */
const CODE_BODY = '```ts\nconst answer = 42\n```'

/** The HTML of `**x**`, as the Markdown worker renders it. */
const X_HTML = '<p><strong>x</strong></p>'

/** Hold the replies of the Markdown worker, so that each body waits until the test releases them. */
function holdMarkdownRenders() {
  return holdWorkerReplies('lib/markdownWorker.ts')
}

/** The text of each render that reaches the Markdown worker from now on. */
function recordRenderRequests(): () => string[] {
  const requests = recordWorkerRequests('lib/markdownWorker.ts')
  return () => requests.map(request => request.text)
}

/**
 * Change the syntax theme. Like every theme change, it empties the HTML caches,
 * renders each body that reads them again, and moves the namespace of the row
 * cache.
 */
async function changeSyntaxTheme(): Promise<void> {
  await setSyntaxTheme(syntaxPairFor('nord'), shikiHighlighter)
}

beforeEach(() => {
  _resetMarkdownCache()
})

afterEach(async () => {
  // The theme is module state, so put the theme of the file back for the next test.
  await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
})

describe('MarkdownText', () => {
  it('shows a skeleton, not the source, while the worker renders the body', async () => {
    holdMarkdownRenders()
    const { container } = render(() => <MarkdownText text={'# Heading\n\nbody'} />)
    await settleWorkerReplies()

    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
    expect(container.textContent).not.toContain('Heading')
    expect(container.querySelector(`.${markdownContent}`)).toBeNull()
  })

  it('replaces the skeleton with the rendered HTML when it lands', async () => {
    const hold = holdMarkdownRenders()
    const { container } = render(() => <MarkdownText text="# Heading" />)
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()

    await hold.release()

    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()
    expect(container.querySelector(`.${markdownContent} h1`)?.textContent).toBe('Heading')
  })

  it('renders a body of any size as Markdown, with no plain-text display', async () => {
    const paragraph = 'paragraph '.repeat(20_000)
    const text = `# Large\n\n${paragraph}`
    const requested = recordRenderRequests()
    const { container } = render(() => <MarkdownText text={text} />)
    await settleWorkerReplies()

    expect(requested()).toEqual([text])
    expect(container.querySelector('h1')?.textContent).toBe('Large')
    // The whole body renders: Markdown drops only the trailing space.
    expect(container.querySelector('p')?.textContent).toBe(paragraph.trimEnd())
    expect(container.textContent).not.toContain('plain-text display')
    expect(container.textContent).not.toContain('Display limited')
  })

  it('applies the caller class and the output marker to the body element', async () => {
    const { container } = render(() => <MarkdownText text="output" class="tool-body" outputPreview />)
    await settleWorkerReplies()

    const body = container.firstElementChild as HTMLElement
    expect(body.className).toBe('tool-body')
    expect(body.getAttribute('data-tool-output-preview')).toBe('')
    expect(body.innerHTML).toBe('<p>output</p>')
  })

  it('puts no output marker on a pending body, so a reader never takes the skeleton for output', async () => {
    holdMarkdownRenders()
    const { container } = render(() => <MarkdownText text="output" outputPreview />)
    await settleWorkerReplies()

    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })

  it.each([
    ['one line', 'single', 1],
    ['three lines', 'a\nb\nc', 3],
    ['six lines', 'a\nb\nc\nd\ne\nf', 6],
    [
      'more than six lines',
      Array.from({ length: 40 }, (_, i) => `line ${i}`).join('\n'),
      6,
    ],
    ['an empty last line', 'a\n', 2],
  ])('sizes the skeleton of %s by its source lines, up to six', (_label, text, lines) => {
    holdMarkdownRenders()
    const { container } = render(() => <MarkdownText text={text} />)
    const skeleton = container.querySelector('[data-testid="markdown-pending"]') as HTMLElement
    expect(skeleton.style.height).toBe(`${lines * SKELETON_LINE_PITCH_PX}px`)
  })

  it('keeps the HTML it showed while the same text renders again with no render cache', async () => {
    // A theme change empties the caches; a body with no row cache -- the file
    // viewer -- must keep its old colours, not turn into a skeleton.
    const [text, setText] = createSignal(CODE_BODY)
    const { container } = render(() => <MarkdownText text={text()} />)
    await settleWorkerReplies()
    const body = () => container.querySelector(`.${markdownContent}`)
    const oldThemeHtml = body()?.innerHTML
    expect(body()?.textContent).toContain('const answer = 42')

    const hold = holdMarkdownRenders()
    await changeSyntaxTheme()
    await settleWorkerReplies()
    // The body renders again under the new theme, and its render waits.
    expect(hold.pending()).toBe(1)
    expect(body()?.innerHTML).toBe(oldThemeHtml)
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()

    await hold.release()
    expect(body()?.innerHTML).not.toBe(oldThemeHtml)
    expect(body()?.textContent).toContain('const answer = 42')

    // A DIFFERENT text never shows the old HTML.
    holdMarkdownRenders()
    setText('# Different')
    expect(body()).toBeNull()
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
  })

  it('reports pending to the row readiness scope until the HTML lands', async () => {
    const { readiness, dispose } = createRoot(disposeRoot => ({
      readiness: createRenderReadinessScope(),
      dispose: disposeRoot,
    }))
    const hold = holdMarkdownRenders()
    render(() => <MarkdownText text="**x**" context={{ readiness }} />)

    expect(readiness.pending()).toBe(true)
    await hold.release()
    expect(readiness.pending()).toBe(false)
    dispose()
  })

  it('draws nothing while pending inside a chat row, whose own skeleton covers it', async () => {
    // The scope lives in a root of its own; the DOM is asserted outside that root,
    // because a root's callback defers DOM updates until it returns.
    const { readiness, dispose } = createRoot(disposeRoot => ({
      readiness: createRenderReadinessScope(),
      dispose: disposeRoot,
    }))
    const hold = holdMarkdownRenders()
    const { container } = render(() => <MarkdownText text={'# Heading\n\nbody'} context={{ readiness }} />)

    expect(container.childNodes).toHaveLength(0)
    await hold.release()
    expect(container.querySelector('h1')?.textContent).toBe('Heading')
    dispose()
  })

  it('stops holding the row when the body unmounts while still pending', () => {
    let readiness: RenderReadinessScope | undefined
    const disposeRoot = createRoot((dispose) => {
      readiness = createRenderReadinessScope()
      return dispose
    })
    holdMarkdownRenders()
    const [mounted, setMounted] = createSignal(true)
    render(() => (
      <Show when={mounted()}>
        <MarkdownText text="**x**" context={{ readiness: readiness! }} />
      </Show>
    ))
    expect(readiness!.pending()).toBe(true)

    setMounted(false)
    expect(readiness!.pending()).toBe(false)
    disposeRoot()
  })

  it('renders an already-cached body at once and never reports pending', async () => {
    requestMarkdownHtml('cached')
    await settleWorkerReplies()
    createRoot((dispose) => {
      const readiness = createRenderReadinessScope()
      const renderCache = createMessageRenderCacheStore().forRow('row:cached')
      const { container } = render(() => <MarkdownText text="cached" context={{ readiness, renderCache }} />)

      expect(container.textContent).toBe('cached')
      expect(readiness.pending()).toBe(false)
      dispose()
    })
  })

  it('renders an empty body at once, and never asks the worker or reports pending', () => {
    const requested = recordRenderRequests()
    createRoot((dispose) => {
      const readiness = createRenderReadinessScope()
      const { container } = render(() => <MarkdownText text="" context={{ readiness }} />)

      const body = container.querySelector(`.${markdownContent}`)
      expect(body).not.toBeNull()
      expect(body?.childNodes).toHaveLength(0)
      expect(readiness.pending()).toBe(false)
      dispose()
    })
    expect(requested()).toEqual([])
  })

  it('looks up the row cache once while it waits for the worker, however often other bodies complete', async () => {
    // Each lookup hashes the whole text, and a pending body reads the worker again
    // at each completion of any other body. The other bodies reach the worker
    // first, so each of their replies lands while this body waits.
    const hold = holdMarkdownRenders()
    const others = Array.from({ length: 5 }, (_, index) => `Another body ${index + 1}.`)
    for (const text of others)
      requestMarkdownHtml(text)
    const renderCache = createMessageRenderCacheStore().forRow('row:pending')
    const lookup = vi.spyOn(renderCache, 'get')
    const { container } = render(() => <MarkdownText text="**x**" context={{ renderCache }} />)
    expect(lookup).toHaveBeenCalledTimes(1)

    for (let completion = 1; completion <= others.length; completion++)
      await hold.releaseNext()
    for (const text of others)
      expect(getCachedMarkdownHtml(text)).toBe(`<p>${text}</p>`)
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
    expect(lookup).toHaveBeenCalledTimes(1)

    // The body still lands, and the row keeps its HTML for the next mount.
    await hold.release()
    expect(container.querySelector('p')?.textContent).toBe('x')
    expect(getCachedRenderValueForString(
      { renderCache },
      markdownCacheNamespace(),
      '**x**',
    )).toBe(X_HTML)
  })

  it('serves the row cache to a new mount without asking the worker', async () => {
    const renderCache = createMessageRenderCacheStore().forRow('row:remount')
    const first = render(() => <MarkdownText text="**x**" context={{ renderCache }} />)
    await settleWorkerReplies()
    expect(first.container.querySelector('p')?.textContent).toBe('x')
    first.unmount()

    // The shared cache forgets the body, so only the row cache holds it.
    _resetMarkdownCache()
    const requested = recordRenderRequests()
    const second = render(() => <MarkdownText text="**x**" context={{ renderCache }} />)
    expect(second.container.querySelector('p')?.textContent).toBe('x')
    expect(requested()).toEqual([])
  })

  it('looks up the row cache again under a new syntax theme', async () => {
    const renderCache = createMessageRenderCacheStore().forRow('row:theme')
    const { container } = render(() => <MarkdownText text={CODE_BODY} context={{ renderCache }} />)
    await settleWorkerReplies()
    const body = () => container.querySelector(`.${markdownContent}`)
    const oldThemeHtml = body()?.innerHTML
    const lookup = vi.spyOn(renderCache, 'get')

    const hold = holdMarkdownRenders()
    await changeSyntaxTheme()
    await settleWorkerReplies()
    expect(lookup).toHaveBeenCalledTimes(1)
    // The old colours stay while the new theme renders.
    expect(body()?.innerHTML).toBe(oldThemeHtml)

    await hold.release()
    expect(body()?.innerHTML).not.toBe(oldThemeHtml)
    expect(body()?.textContent).toContain('const answer = 42')
    expect(lookup).toHaveBeenCalledTimes(1)
  })
})

describe('MarkdownPrefetch', () => {
  it('renders nothing and starts a low-priority render of its text', async () => {
    const gate = fillMarkdownRenderGate()
    const { container } = render(() => <MarkdownPrefetch text="collapsed body" />)
    requestMarkdownHtml('A visible body.')

    expect(container.childNodes).toHaveLength(0)
    // The worker receives the prefetch after a body that a reader needs now.
    expect(await gate.release()).toEqual(['A visible body.', 'collapsed body'])
    expect(getCachedMarkdownHtml('collapsed body')).toBe('<p>collapsed body</p>')
  })

  it('prefetches the new text when the text changes', () => {
    const requested = recordRenderRequests()
    const [text, setText] = createSignal('first')
    render(() => <MarkdownPrefetch text={text()} />)
    setText('second')

    expect(requested()).toEqual(['first', 'second'])
  })

  it('prefetches the text again after a syntax theme change empties the HTML cache', async () => {
    const requested = recordRenderRequests()
    render(() => <MarkdownPrefetch text="collapsed body" />)
    await settleWorkerReplies()

    await changeSyntaxTheme()
    await settleWorkerReplies()

    expect(requested()).toEqual(['collapsed body', 'collapsed body'])
    expect(getCachedMarkdownHtml('collapsed body')).toBe('<p>collapsed body</p>')
  })

  it('prefetches in the visible copy of a row', () => {
    const requested = recordRenderRequests()
    render(() => <MarkdownPrefetch text="collapsed body" context={{ premeasureMode: false }} />)

    expect(requested()).toEqual(['collapsed body'])
  })

  it('prefetches nothing in the hidden premeasure copy of a row, which the reader cannot expand', async () => {
    const requested = recordRenderRequests()
    render(() => <MarkdownPrefetch text="collapsed body" context={{ premeasureMode: true }} />)
    await changeSyntaxTheme()
    await settleWorkerReplies()

    expect(requested()).toEqual([])
    expect(getCachedMarkdownHtml('collapsed body')).toBeUndefined()
  })
})
