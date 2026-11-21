import type { MarkdownLocalLinkContextValue } from './markdownLocalLinks'
import type { RenderReadinessScope } from './renderReadiness'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createEffect, createRoot, createSignal, Show } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { workerFile } from '~/api/workerRpc'
import { dismissActiveTooltip, HIDE_DELAY_MS, SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { ReadFileResponseSchema } from '~/generated/proto/leapmux/v1/file_pb'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import {
  LOCAL_IMAGE_ATTRIBUTE,
  LOCAL_IMAGE_CLICKABLE_ATTRIBUTE,
  LOCAL_IMAGE_ERROR_CLASS,
  LOCAL_IMAGE_FALLBACK_CLASS,
  LOCAL_LINK_ATTRIBUTE,
  LOCAL_LINKS_ACTIVE_ATTRIBUTE,
} from '~/lib/localLinks'
import { _resetLocalMarkdownImageCacheForTest, acquireLocalMarkdownImage, clearLocalMarkdownImageCache } from '~/lib/localMarkdownImages'
import { _resetMarkdownCache, getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { shikiHighlighter } from '~/lib/shikiSyncHighlighter'
import { syntaxPairFor } from '~/lib/syntaxThemes'
import { setSyntaxTheme } from '~/lib/syntaxThemeStore'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { deferred } from '~/test-support/async'
import { holdWorkerReplies, recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { fillMarkdownRenderGate } from '~/test-support/markdownRenderGate'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { MarkdownLocalLinkProvider } from './markdownLocalLinks'
import { markdownCacheNamespace } from './markdownRendering'
import { MarkdownPrefetch, MarkdownText } from './MarkdownText'
import { createMessageRenderCacheStore, getCachedRenderValueForString } from './messageRenderCache'
import { createRenderReadinessScope, READINESS_REQUEST_LIMIT_MS } from './renderReadiness'
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

/** Minimal valid PNG bytes with 24x12 IHDR dimensions. */
function makePngBytes(width = 24, height = 12): Uint8Array {
  const bytes = new Uint8Array(64)
  bytes.set([
    0x89,
    0x50,
    0x4E,
    0x47,
    0x0D,
    0x0A,
    0x1A,
    0x0A,
  ], 0)
  bytes[11] = 13
  bytes.set([0x49, 0x48, 0x44, 0x52], 12)
  const view = new DataView(bytes.buffer)
  view.setUint32(16, width, false)
  view.setUint32(20, height, false)
  return bytes
}

function makeReadFileResponse(bytes: Uint8Array, totalSize?: bigint) {
  return create(ReadFileResponseSchema, {
    content: bytes,
    totalSize: totalSize ?? BigInt(bytes.length),
  })
}

function makeLocalLinkContext(
  overrides: Partial<MarkdownLocalLinkContextValue> = {},
): MarkdownLocalLinkContextValue {
  return {
    workerId: 'w-1',
    baseDir: '/repo/docs',
    workingDir: '/repo',
    homeDir: '/home/alice',
    flavor: 'posix',
    onActivate: vi.fn(),
    ...overrides,
  }
}

describe('MarkdownText local links and images', () => {
  let blobCounter = 0
  let readFileSpy: ReturnType<typeof vi.spyOn>
  let revokeObjectUrlSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    blobCounter = 0
    _resetLocalMarkdownImageCacheForTest()
    vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:test-local-img-${++blobCounter}`)
    revokeObjectUrlSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    readFileSpy = vi.spyOn(workerFile, 'readFile').mockResolvedValue(makeReadFileResponse(makePngBytes()))
  })

  afterEach(() => {
    dismissActiveTooltip()
    _resetLocalMarkdownImageCacheForTest()
    window.getSelection()?.removeAllRanges()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('keeps local links and local images inert without a provider or with an empty workerId', async () => {
    const onActivate = vi.fn()
    const first = render(() => <MarkdownText text="[Guide](./guide.md) ![Diagram](./arch.png)" />)
    await settleWorkerReplies()

    const bodyNoProvider = first.container.querySelector(`.${markdownContent}`) as HTMLElement
    expect(bodyNoProvider.hasAttribute(LOCAL_LINKS_ACTIVE_ATTRIBUTE)).toBe(false)
    const link = bodyNoProvider.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement
    expect(link.getAttribute('role')).toBeNull()
    expect(link.getAttribute('tabindex')).toBeNull()
    fireEvent.click(link)
    expect(readFileSpy).not.toHaveBeenCalled()
    expect(bodyNoProvider.querySelector('img')).toBeNull()
    expect(bodyNoProvider.querySelector(`.${LOCAL_IMAGE_FALLBACK_CLASS}`)?.textContent).toBe('Diagram')
    first.unmount()

    const emptyWorkerCtx = makeLocalLinkContext({ workerId: '', onActivate })
    const second = render(() => (
      <MarkdownLocalLinkProvider value={emptyWorkerCtx}>
        <MarkdownText text="[Guide](./guide.md) ![Diagram](./arch.png)" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()

    const bodyEmpty = second.container.querySelector(`.${markdownContent}`) as HTMLElement
    expect(bodyEmpty.hasAttribute(LOCAL_LINKS_ACTIVE_ATTRIBUTE)).toBe(false)
    const linkEmpty = bodyEmpty.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement
    expect(linkEmpty.getAttribute('role')).toBeNull()
    expect(linkEmpty.getAttribute('tabindex')).toBeNull()
    fireEvent.click(linkEmpty)
    expect(onActivate).not.toHaveBeenCalled()
    expect(readFileSpy).not.toHaveBeenCalled()
  })

  it('activates local links on primary click, Enter, and Space while ignoring blocked inputs and selections', async () => {
    const onActivate = vi.fn()
    const ctx = makeLocalLinkContext({ onActivate })
    const { container } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text="Before [Open `guide`](./guide.md) after" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()

    const body = container.querySelector(`.${markdownContent}`) as HTMLElement
    expect(body.hasAttribute(LOCAL_LINKS_ACTIVE_ATTRIBUTE)).toBe(true)
    const link = body.querySelector(`[${LOCAL_LINK_ATTRIBUTE}="./guide.md"]`) as HTMLElement
    expect(link.getAttribute('role')).toBe('button')
    expect(link.getAttribute('tabindex')).toBe('0')

    // Primary click on nested `<code>` activates the enclosing local link.
    fireEvent.click(link.querySelector('code')!)
    expect(onActivate).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenLastCalledWith('./guide.md')

    // Enter and Space prevent default and stop propagation.
    const parentKeySpy = vi.fn()
    container.addEventListener('keydown', parentKeySpy)

    const enterEv = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    link.dispatchEvent(enterEv)
    expect(enterEv.defaultPrevented).toBe(true)
    expect(parentKeySpy).not.toHaveBeenCalled()
    expect(onActivate).toHaveBeenCalledTimes(2)

    const spaceEv = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    link.dispatchEvent(spaceEv)
    expect(spaceEv.defaultPrevented).toBe(true)
    expect(parentKeySpy).not.toHaveBeenCalled()
    expect(onActivate).toHaveBeenCalledTimes(3)
    container.removeEventListener('keydown', parentKeySpy)

    // Ignored clicks: non-primary button and defaultPrevented.
    fireEvent.click(link, { button: 1 })
    const preventedClick = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })
    preventedClick.preventDefault()
    link.dispatchEvent(preventedClick)
    expect(onActivate).toHaveBeenCalledTimes(3)

    // Ignored keydowns: repeat, modifiers, and Shift+Space.
    fireEvent.keyDown(link, { key: 'Enter', repeat: true })
    fireEvent.keyDown(link, { key: 'Enter', ctrlKey: true })
    fireEvent.keyDown(link, { key: 'Enter', metaKey: true })
    fireEvent.keyDown(link, { key: 'Enter', altKey: true })
    fireEvent.keyDown(link, { key: ' ', shiftKey: true })
    expect(onActivate).toHaveBeenCalledTimes(3)

    // Ignored click when a non-collapsed text selection anchors or focuses inside the body.
    const p = body.querySelector('p')!
    const beforeOutside = document.createElement('span')
    beforeOutside.textContent = 'before text'
    document.body.prepend(beforeOutside)
    const afterOutside = document.createElement('span')
    afterOutside.textContent = 'after text'
    document.body.appendChild(afterOutside)
    try {
      // Cross-element selection ending inside the body (focusNode inside, anchorNode outside).
      const sel = window.getSelection()!
      const intoBody = document.createRange()
      intoBody.setStart(beforeOutside.firstChild!, 0)
      intoBody.setEnd(p.firstChild!, 4)
      sel.removeAllRanges()
      sel.addRange(intoBody)
      expect(sel.isCollapsed).toBe(false)
      fireEvent.click(link)
      expect(onActivate).toHaveBeenCalledTimes(3)

      // Cross-element selection starting inside the body (anchorNode inside, focusNode outside).
      const outOfBody = document.createRange()
      outOfBody.setStart(p.firstChild!, 0)
      outOfBody.setEnd(afterOutside.firstChild!, 5)
      sel.removeAllRanges()
      sel.addRange(outOfBody)
      expect(sel.isCollapsed).toBe(false)
      fireEvent.click(link)
      expect(onActivate).toHaveBeenCalledTimes(3)
    }
    finally {
      window.getSelection()?.removeAllRanges()
      beforeOutside.remove()
      afterOutside.remove()
    }
  })

  it('delegates hover and focus tooltips with HIDE_DELAY_MS, ignores touch and child transitions, and dismisses on click', async () => {
    const onActivate = vi.fn()
    const ctx = makeLocalLinkContext({ onActivate })
    const { container } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text="See [`app.ts` **entry**](./app.ts:42)" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()

    vi.useFakeTimers()
    const body = container.querySelector(`.${markdownContent}`) as HTMLElement
    const link = body.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement
    const code = link.querySelector('code') as HTMLElement
    const strong = link.querySelector('strong') as HTMLElement
    expect(link.hasAttribute('title')).toBe(false)

    // Touch pointerover does not open a tooltip.
    link.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'touch' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS + 100)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()

    // Mouse pointerover opens tooltip after SHOW_DELAY_MS without resetting when moving across nested children.
    link.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS / 2)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    code.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: strong }))
    strong.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: code }))
    vi.advanceTimersByTime(SHOW_DELAY_MS / 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')

    // Moving across child nodes inside the same link keeps the visible tooltip open.
    code.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: strong }))
    strong.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: code }))
    vi.advanceTimersByTime(HIDE_DELAY_MS * 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')

    // Leaving the link preserves the tooltip across HIDE_DELAY_MS; returning keeps it open without SHOW_DELAY_MS.
    link.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(HIDE_DELAY_MS / 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')
    link.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(HIDE_DELAY_MS * 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')

    // Focus keeps tooltip open even after pointer leaves.
    fireEvent.focusIn(link)
    link.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: body }))
    vi.advanceTimersByTime(HIDE_DELAY_MS * 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')

    // Clicking dismisses the tooltip immediately, and inner child transitions do not re-arm it.
    fireEvent.click(link)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    expect(onActivate).toHaveBeenCalledWith('./app.ts:42')
    code.dispatchEvent(new PointerEvent('pointerout', { bubbles: true, pointerType: 'mouse', relatedTarget: strong }))
    strong.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse', relatedTarget: code }))
    vi.advanceTimersByTime(SHOW_DELAY_MS + 100)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()

    // Keyboard focusIn opens the tooltip from a closed state after SHOW_DELAY_MS, ignores
    // intra-element focusOut to a child, and closes after HIDE_DELAY_MS on focusOut outside.
    fireEvent.focusIn(link)
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')
    link.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: code }))
    vi.advanceTimersByTime(HIDE_DELAY_MS * 2)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/app.ts')
    link.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: body }))
    vi.advanceTimersByTime(HIDE_DELAY_MS)
    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  it('closes an enclosing open popover when a local link inside it is activated', async () => {
    const onActivate = vi.fn()
    const ctx = makeLocalLinkContext({ onActivate })
    const { container } = render(() => (
      <div popover="auto" data-testid="host-popover">
        <MarkdownLocalLinkProvider value={ctx}>
          <MarkdownText text="[Doc](./doc.md)" />
        </MarkdownLocalLinkProvider>
      </div>
    ))
    await settleWorkerReplies()

    const popover = container.querySelector('[data-testid="host-popover"]') as HTMLElement
    popover.showPopover()
    const hideSpy = vi.spyOn(popover, 'hidePopover')
    const link = popover.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement

    fireEvent.click(link)
    expect(hideSpy).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenCalledWith('./doc.md')
  })

  it('hydrates standalone local images into clickable inline images and keeps linked images non-clickable', async () => {
    const onActivate = vi.fn()
    const ctx = makeLocalLinkContext({ onActivate })
    const { container } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text="![Solo](./solo.png) [![External](./ext.png)](https://example.com) [![Local](./loc.png)](./doc.md)" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()

    await waitFor(() => {
      expect(container.querySelectorAll('img')).toHaveLength(3)
    })

    const soloSpan = container.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}="./solo.png"]`) as HTMLElement
    expect(soloSpan.hasAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)).toBe(true)
    expect(soloSpan.getAttribute('role')).toBe('button')
    expect(soloSpan.getAttribute('tabindex')).toBe('0')
    const soloImg = soloSpan.querySelector('img') as HTMLImageElement
    expect(soloImg.getAttribute('src')).toMatch(/^blob:test-local-img-/u)
    expect(soloImg.getAttribute('alt')).toBe('Solo')
    expect(soloImg.getAttribute('width')).toBe('24')
    expect(soloImg.getAttribute('height')).toBe('12')

    fireEvent.click(soloImg)
    expect(onActivate).toHaveBeenLastCalledWith('./solo.png')
    fireEvent.keyDown(soloSpan, { key: 'Enter' })
    expect(onActivate).toHaveBeenCalledTimes(2)
    fireEvent.keyDown(soloSpan, { key: ' ' })
    expect(onActivate).toHaveBeenCalledTimes(3)

    // Hovering standalone image opens tooltip with resolved path.
    vi.useFakeTimers()
    soloSpan.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/solo.png')
    vi.useRealTimers()

    // Image inside external <a> has no local-image clickable attributes and does not trigger local onActivate.
    const extSpan = container.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}="./ext.png"]`) as HTMLElement
    expect(extSpan.hasAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)).toBe(false)
    expect(extSpan.getAttribute('role')).toBeNull()
    expect(extSpan.getAttribute('tabindex')).toBeNull()
    const extAnchor = extSpan.closest('a') as HTMLAnchorElement
    extAnchor.addEventListener('click', e => e.preventDefault())
    fireEvent.click(extSpan.querySelector('img')!)
    expect(onActivate).toHaveBeenCalledTimes(3)

    // Image inside local link delegates clicks to the enclosing local link.
    const locSpan = container.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}="./loc.png"]`) as HTMLElement
    expect(locSpan.hasAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)).toBe(false)
    expect(locSpan.getAttribute('role')).toBeNull()
    fireEvent.click(locSpan.querySelector('img')!)
    expect(onActivate).toHaveBeenCalledTimes(4)
    expect(onActivate).toHaveBeenLastCalledWith('./doc.md')
  })

  it('hydrates an already-cached local image synchronously without reporting readiness pending', async () => {
    const ctx = makeLocalLinkContext()
    const pre = await acquireLocalMarkdownImage({
      workerId: ctx.workerId,
      rawSrc: './cached.png',
      baseDir: ctx.baseDir,
      workingDir: ctx.workingDir,
      homeDir: ctx.homeDir,
      flavor: ctx.flavor,
    })
    expect(pre.result.kind).toBe('ok')
    pre.release()

    readFileSpy.mockClear()
    requestMarkdownHtml('![Cached](./cached.png)')
    await settleWorkerReplies()

    let everPending = false
    let readiness!: RenderReadinessScope
    const disposeRoot = createRoot((dispose) => {
      readiness = createRenderReadinessScope()
      createEffect(() => {
        if (readiness.pending())
          everPending = true
      })
      return dispose
    })
    try {
      const { container } = render(() => (
        <MarkdownLocalLinkProvider value={ctx}>
          <MarkdownText text="![Cached](./cached.png)" context={{ readiness }} />
        </MarkdownLocalLinkProvider>
      ))
      expect(readiness.pending()).toBe(false)
      expect(everPending).toBe(false)
      expect(container.querySelector('img')?.getAttribute('src')).toMatch(/^blob:test-local-img-/u)
      expect(readFileSpy).not.toHaveBeenCalled()
    }
    finally {
      disposeRoot()
    }
  })

  it('shows fallback label and error reason when a local image is oversized or unreadable', async () => {
    readFileSpy.mockResolvedValue(
      makeReadFileResponse(new Uint8Array(0), BigInt(MAX_FILE_IMAGE_BYTES + 1)),
    )
    const ctx = makeLocalLinkContext()
    const { container } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text="![Huge diagram](./huge.png)" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()

    await waitFor(() => {
      expect(container.querySelector(`.${LOCAL_IMAGE_ERROR_CLASS}`)).not.toBeNull()
    })
    expect(container.querySelector(`.${LOCAL_IMAGE_FALLBACK_CLASS}`)?.textContent).toBe('Huge diagram')
    expect(container.querySelector(`.${LOCAL_IMAGE_ERROR_CLASS}`)?.textContent).toContain('too large')
    expect(container.querySelector('img')).toBeNull()
  })

  it('restores inert links and fallback images and dismisses tooltips when workerId deactivates', async () => {
    readFileSpy.mockImplementation(async (_workerId: string, req: { path?: string }) => {
      if (req.path?.endsWith('broken.png'))
        return makeReadFileResponse(new Uint8Array(), 0n)
      return makeReadFileResponse(makePngBytes())
    })

    const [workerId, setWorkerId] = createSignal('w-1')
    const ctx: MarkdownLocalLinkContextValue = {
      get workerId() { return workerId() },
      baseDir: '/repo/docs',
      workingDir: '/repo',
      homeDir: '/home/alice',
      flavor: 'posix',
      onActivate: vi.fn(),
    }
    const { container } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text="[Note](./note.md) ![Chart](./chart.png) ![Broken](./broken.png)" />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()
    const chartSpan = container.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}="./chart.png"]`) as HTMLElement
    const brokenSpan = container.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}="./broken.png"]`) as HTMLElement
    await waitFor(() => {
      expect(chartSpan.querySelector('img')).not.toBeNull()
      expect(brokenSpan.querySelector(`.${LOCAL_IMAGE_ERROR_CLASS}`)).not.toBeNull()
    })

    vi.useFakeTimers()
    const link = container.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement
    link.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/note.md')

    setWorkerId('')

    const body = container.querySelector(`.${markdownContent}`) as HTMLElement
    expect(body.hasAttribute(LOCAL_LINKS_ACTIVE_ATTRIBUTE)).toBe(false)
    expect(link.getAttribute('role')).toBeNull()
    expect(link.getAttribute('tabindex')).toBeNull()

    expect(chartSpan.hasAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)).toBe(false)
    expect(chartSpan.getAttribute('role')).toBeNull()
    expect(chartSpan.getAttribute('tabindex')).toBeNull()
    expect(chartSpan.querySelector('img')).toBeNull()
    expect(chartSpan.querySelector(`.${LOCAL_IMAGE_FALLBACK_CLASS}`)?.textContent).toBe('Chart')

    expect(brokenSpan.hasAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)).toBe(false)
    expect(brokenSpan.getAttribute('role')).toBeNull()
    expect(brokenSpan.getAttribute('tabindex')).toBeNull()
    expect(brokenSpan.querySelector(`.${LOCAL_IMAGE_ERROR_CLASS}`)).toBeNull()
    expect(brokenSpan.querySelector(`.${LOCAL_IMAGE_FALLBACK_CLASS}`)?.textContent).toBe('Broken')

    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  it('re-upgrades links, re-hydrates images, releases prior leases, and clears detached tooltip targets on HTML updates', async () => {
    const [text, setText] = createSignal('[One](./one.md) ![First](./first.png)')
    const ctx = makeLocalLinkContext()
    const { container, unmount } = render(() => (
      <MarkdownLocalLinkProvider value={ctx}>
        <MarkdownText text={text()} />
      </MarkdownLocalLinkProvider>
    ))
    await settleWorkerReplies()
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
    const firstUrl = container.querySelector('img')!.getAttribute('src')!

    vi.useFakeTimers()
    const linkOne = container.querySelector(`[${LOCAL_LINK_ATTRIBUTE}="./one.md"]`) as HTMLElement
    linkOne.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse' }))
    vi.advanceTimersByTime(SHOW_DELAY_MS)
    expect(screen.getByRole('tooltip', { hidden: true })).toHaveTextContent('/repo/docs/one.md')
    vi.useRealTimers()

    // In-place replacement: pre-render next HTML so the body stays mounted without entering pending.
    const nextText = '[Two](./two.md) ![Second](./second.png)'
    requestMarkdownHtml(nextText)
    await settleWorkerReplies()
    setText(nextText)

    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
    const linkTwo = container.querySelector(`[${LOCAL_LINK_ATTRIBUTE}="./two.md"]`) as HTMLElement
    expect(linkTwo.getAttribute('role')).toBe('button')
    expect(linkTwo.getAttribute('tabindex')).toBe('0')
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('alt')).toBe('Second'))
    const secondUrl = container.querySelector('img')!.getAttribute('src')!

    // Transition through pending (unmount and remount of the body <div>).
    const hold = holdMarkdownRenders()
    setText('[Three](./three.md) ![Third](./third.png)')
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
    await hold.release()

    const linkThree = container.querySelector(`[${LOCAL_LINK_ATTRIBUTE}="./three.md"]`) as HTMLElement
    expect(linkThree.getAttribute('role')).toBe('button')
    expect(linkThree.getAttribute('tabindex')).toBe('0')
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('alt')).toBe('Third'))
    const thirdUrl = container.querySelector('img')!.getAttribute('src')!

    // Clearing the cache immediately revokes evicted entries whose leases were released,
    // while keeping the currently mounted image's lease alive until unmount.
    clearLocalMarkdownImageCache()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(firstUrl)
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(secondUrl)
    expect(revokeObjectUrlSpy).not.toHaveBeenCalledWith(thirdUrl)

    unmount()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(thirdUrl)
  })

  it('tracks cache-miss image readiness in premeasureMode, omits tabindex and tooltip, and releases at READINESS_REQUEST_LIMIT_MS', async () => {
    const gate = deferred<ReturnType<typeof makeReadFileResponse>>()
    readFileSpy.mockReturnValueOnce(gate.promise)

    const ctx = makeLocalLinkContext()
    requestMarkdownHtml('[Guide](./guide.md) ![Photo](./photo.png)')
    await settleWorkerReplies()

    let readiness!: RenderReadinessScope
    const disposeRoot = createRoot((dispose) => {
      readiness = createRenderReadinessScope()
      return dispose
    })
    try {
      const { container } = render(() => (
        <MarkdownLocalLinkProvider value={ctx}>
          <MarkdownText
            text="[Guide](./guide.md) ![Photo](./photo.png)"
            context={{ readiness, premeasureMode: true }}
          />
        </MarkdownLocalLinkProvider>
      ))

      const body = container.querySelector(`.${markdownContent}`) as HTMLElement
      expect(body).not.toBeNull()
      const link = body.querySelector(`[${LOCAL_LINK_ATTRIBUTE}]`) as HTMLElement
      const imgSpan = body.querySelector(`[${LOCAL_IMAGE_ATTRIBUTE}]`) as HTMLElement
      expect(link.getAttribute('role')).toBe('button')
      expect(link.getAttribute('tabindex')).toBeNull()
      expect(imgSpan.getAttribute('tabindex')).toBeNull()
      expect(readiness.pending()).toBe(true)

      // Resolving the image settles readiness without unmounting the body div.
      gate.resolve(makeReadFileResponse(makePngBytes()))
      await waitFor(() => expect(readiness.pending()).toBe(false))
      expect(container.querySelector(`.${markdownContent}`)).toBe(body)
      expect(body.querySelector('img')).not.toBeNull()

      // Hovering in premeasureMode mounts no tooltip.
      vi.useFakeTimers()
      link.dispatchEvent(new PointerEvent('pointerover', { bubbles: true, pointerType: 'mouse' }))
      vi.advanceTimersByTime(SHOW_DELAY_MS + 100)
      expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
      vi.useRealTimers()
    }
    finally {
      disposeRoot()
    }

    // Failing image settles readiness cleanly too.
    readFileSpy.mockResolvedValueOnce(makeReadFileResponse(new Uint8Array(0), 0n))
    requestMarkdownHtml('![Broken](./broken.png)')
    await settleWorkerReplies()
    let failReadiness!: RenderReadinessScope
    const disposeFail = createRoot((dispose) => {
      failReadiness = createRenderReadinessScope()
      return dispose
    })
    try {
      render(() => (
        <MarkdownLocalLinkProvider value={ctx}>
          <MarkdownText
            text="![Broken](./broken.png)"
            context={{ readiness: failReadiness, premeasureMode: true }}
          />
        </MarkdownLocalLinkProvider>
      ))
      await waitFor(() => expect(failReadiness.pending()).toBe(false))
    }
    finally {
      disposeFail()
    }

    // Hung read stops holding readiness after READINESS_REQUEST_LIMIT_MS.
    vi.useFakeTimers()
    readFileSpy.mockReturnValueOnce(new Promise(() => {}))
    requestMarkdownHtml('![Hung](./hung.png)')
    await settleWorkerReplies()
    let hungReadiness!: RenderReadinessScope
    const disposeHung = createRoot((dispose) => {
      hungReadiness = createRenderReadinessScope()
      return dispose
    })
    try {
      render(() => (
        <MarkdownLocalLinkProvider value={ctx}>
          <MarkdownText
            text="![Hung](./hung.png)"
            context={{ readiness: hungReadiness, premeasureMode: true }}
          />
        </MarkdownLocalLinkProvider>
      ))
      expect(hungReadiness.pending()).toBe(true)
      vi.advanceTimersByTime(READINESS_REQUEST_LIMIT_MS)
      expect(hungReadiness.pending()).toBe(false)
    }
    finally {
      disposeHung()
    }
  })
})
