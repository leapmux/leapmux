import type { RenderReadinessScope } from './renderReadiness'
import { render } from '@solidjs/testing-library'
import { createRoot, createSignal, Show } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { prefetchMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { MarkdownPrefetch, MarkdownText } from './messageRenderers'
import { createRenderReadinessScope } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX } from './widgets/ChatRowSkeleton'

// The worker bridge is replaced by a signal the test drives: each test states what
// the worker has rendered so far, and MarkdownText re-reads it reactively, as it
// re-reads the real cache when the version signal bumps.
const [workerHtml, setWorkerHtml] = createSignal<string | undefined>(undefined)

vi.mock('~/lib/renderMarkdown', () => ({
  getCachedMarkdownHtml: vi.fn(() => undefined),
  // eslint-disable-next-line solid/reactivity -- MarkdownText calls the mock inside its memo
  requestMarkdownHtml: vi.fn(() => workerHtml()),
  prefetchMarkdownHtml: vi.fn(),
}))

const requestMock = vi.mocked(requestMarkdownHtml)
const prefetchMock = vi.mocked(prefetchMarkdownHtml)

describe('MarkdownText', () => {
  beforeEach(() => {
    setWorkerHtml(undefined)
    requestMock.mockClear()
    prefetchMock.mockClear()
  })

  it('shows a skeleton, not the source, while the worker renders the body', () => {
    const { container } = render(() => <MarkdownText text={'# Heading\n\nbody'} />)

    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
    expect(container.textContent).not.toContain('Heading')
    expect(container.querySelector(`.${markdownContent}`)).toBeNull()
  })

  it('replaces the skeleton with the rendered HTML when it lands', () => {
    const { container } = render(() => <MarkdownText text="# Heading" />)
    setWorkerHtml('<h1>Heading</h1>')

    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()
    expect(container.querySelector(`.${markdownContent} h1`)?.textContent).toBe('Heading')
  })

  it('renders a body of any size as Markdown, with no plain-text display', () => {
    const text = `# Large\n\n${'paragraph '.repeat(20_000)}`
    setWorkerHtml('<h1>Large</h1><p>rendered</p>')
    const { container } = render(() => <MarkdownText text={text} />)

    expect(requestMock).toHaveBeenCalledWith(text, undefined)
    expect(container.querySelector('h1')?.textContent).toBe('Large')
    expect(container.textContent).not.toContain('plain-text display')
    expect(container.textContent).not.toContain('Display limited')
  })

  it('applies the caller class and the output marker to the body element', () => {
    setWorkerHtml('<p>output</p>')
    const { container } = render(() => <MarkdownText text="output" class="tool-body" outputPreview />)

    const body = container.firstElementChild as HTMLElement
    expect(body.className).toBe('tool-body')
    expect(body.getAttribute('data-tool-output-preview')).toBe('')
  })

  it('puts no output marker on a pending body, so a reader never takes the skeleton for output', () => {
    const { container } = render(() => <MarkdownText text="output" outputPreview />)
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
    const { container } = render(() => <MarkdownText text={text} />)
    const skeleton = container.querySelector('[data-testid="markdown-pending"]') as HTMLElement
    expect(skeleton.style.height).toBe(`${lines * SKELETON_LINE_PITCH_PX}px`)
  })

  it('keeps the HTML it showed while the same text renders again with no render cache', () => {
    // A theme change empties the caches; a body with no row cache -- the file
    // viewer -- must keep its old colours, not turn into a skeleton.
    const [text, setText] = createSignal('# Same')
    setWorkerHtml('<h1>old theme</h1>')
    const { container } = render(() => <MarkdownText text={text()} />)
    expect(container.querySelector('h1')?.textContent).toBe('old theme')

    setWorkerHtml(undefined)
    expect(container.querySelector('h1')?.textContent).toBe('old theme')
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeNull()

    setWorkerHtml('<h1>new theme</h1>')
    expect(container.querySelector('h1')?.textContent).toBe('new theme')

    // A DIFFERENT text never shows the old HTML.
    setWorkerHtml(undefined)
    setText('# Different')
    expect(container.querySelector('h1')).toBeNull()
    expect(container.querySelector('[data-testid="markdown-pending"]')).toBeInTheDocument()
  })

  it('reports pending to the row readiness scope until the HTML lands', () => {
    createRoot((dispose) => {
      const readiness = createRenderReadinessScope()
      render(() => <MarkdownText text="**x**" context={{ readiness }} />)

      expect(readiness.pending()).toBe(true)
      setWorkerHtml('<p>x</p>')
      expect(readiness.pending()).toBe(false)
      dispose()
    })
  })

  it('stops holding the row when the body unmounts while still pending', () => {
    let readiness: RenderReadinessScope | undefined
    const disposeRoot = createRoot((dispose) => {
      readiness = createRenderReadinessScope()
      return dispose
    })
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

  it('renders an already-cached body at once and never reports pending', () => {
    setWorkerHtml('<p>cached</p>')
    createRoot((dispose) => {
      const readiness = createRenderReadinessScope()
      const renderCache = createMessageRenderCacheStore().forRow('row:cached')
      const { container } = render(() => <MarkdownText text="cached" context={{ readiness, renderCache }} />)

      expect(container.textContent).toBe('cached')
      expect(readiness.pending()).toBe(false)
      dispose()
    })
  })
})

describe('MarkdownPrefetch', () => {
  beforeEach(() => {
    prefetchMock.mockClear()
  })

  it('renders nothing and starts a low-priority render of its text', () => {
    const { container } = render(() => <MarkdownPrefetch text="collapsed body" />)

    expect(container.childNodes).toHaveLength(0)
    expect(prefetchMock).toHaveBeenCalledWith('collapsed body')
  })

  it('prefetches the new text when the text changes', () => {
    const [text, setText] = createSignal('first')
    render(() => <MarkdownPrefetch text={text()} />)
    setText('second')

    expect(prefetchMock.mock.calls).toEqual([
      ['first'],
      ['second'],
    ])
  })
})
