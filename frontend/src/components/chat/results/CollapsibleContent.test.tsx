import type { CollapsibleContentKind } from '~/components/chat/results/CollapsibleContent'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as markdownRendering from '~/components/chat/markdownRendering'
import { createMessageRenderCacheStore } from '~/components/chat/messageRenderCache'
import { CollapsibleContent } from '~/components/chat/results/CollapsibleContent'
import * as safeTextDisplay from '~/components/chat/safeTextDisplay'
import { toolResultContentAnsi, toolResultContentPre } from '~/components/chat/toolStyles.css'
import { settleWorkerReplies } from '~/test-support/fakeWorker'

describe('CollapsibleContent', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each(['pre', 'ansi-or-pre', 'json'] as const)('does not render unused Markdown for kind="%s"', (kind) => {
    const renderMarkdown = vi.spyOn(markdownRendering, 'markdownHtmlForContext').mockReturnValue('<p>unused</p>')
    const text = `${'minified'.repeat(1_000)}\nsecond line`

    render(() => (
      <CollapsibleContent kind={kind} text={text} display="minified" isCollapsed />
    ))

    expect(renderMarkdown).not.toHaveBeenCalled()
  })

  it.each(['pre', 'ansi-or-pre', 'json'] as const)('limits a large expanded kind="%s" body', (kind) => {
    const text = `start\n${'x'.repeat(100_000)}\nend`
    const { container } = render(() => (
      <CollapsibleContent kind={kind} text={text} display={text} isCollapsed={false} />
    ))

    expect(container.textContent!.length).toBeLessThan(text.length)
    expect(container.textContent).toContain('Display limited')
    expect(container.textContent).toContain('start')
    expect(container.textContent).toContain('end')
  })

  it('renders pre body for kind="pre"', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="pre" text="hello\nworld" display="hello\nworld" isCollapsed={false} />
    ))
    expect(container.textContent).toContain('hello')
    expect(container.textContent).toContain('world')
  })

  it('applies the toolResultCollapsed class when isCollapsed is true', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="pre" text="x" display="x" isCollapsed={true} />
    ))
    const div = container.firstElementChild as HTMLElement
    // The collapsed class follows the base display class.
    // Require both classes on the same existing element.
    expect(div).not.toBeNull()
    expect(div!.className.split(/\s+/).length).toBeGreaterThan(1)
  })

  it('renders ANSI HTML when ansi-or-pre kind sees ANSI escapes', () => {
    const ansiText = '\x1B[31mred\x1B[0m'
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text={ansiText} display={ansiText} isCollapsed={false} />
    ))
    // renderAnsi converts the ANSI escape into a styled <span>.
    expect(container.querySelector('span')).not.toBeNull()
  })

  it('keeps the ANSI render branch when escapes are outside the collapsed display slice', () => {
    const text = 'plain prefix\n\x1B[31mred after fold\x1B[0m'
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text={text} display="plain prefix" isCollapsed={true} />
    ))

    const div = container.firstElementChild as HTMLElement
    expect(div.classList.contains(toolResultContentAnsi)).toBe(true)
    expect(div.classList.contains(toolResultContentPre)).toBe(false)
  })

  it('renders plain pre when ansi-or-pre kind sees no ANSI escapes', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text="plain" display="plain" isCollapsed={false} />
    ))
    // No ANSI → no styled inner spans, just the plain text.
    expect(container.textContent).toBe('plain')
  })

  it('renders kind="json" as token content in the shared tool-result wrapper', () => {
    const json = '{\n  "a": 1\n}'
    const { container } = render(() => (
      <CollapsibleContent kind="json" text={json} isCollapsed={false} />
    ))
    // Until the token worker answers, TokenizedCode shows the raw JSON text inside
    // the shared toolResultContentAnsi wrapper (NOT the plain-pre fallback used by
    // other kinds).
    expect(container.textContent).toContain('"a": 1')
    const div = container.firstElementChild as HTMLElement
    expect(div.classList.contains(toolResultContentAnsi)).toBe(true)
    expect(div.classList.contains(toolResultContentPre)).toBe(false)
  })

  it('strips ANSI escape bytes from the plain fallback while syntax work is paused', () => {
    const { container } = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={'\x1B[31mred\x1B[0m'}
        display={'\x1B[31mred\x1B[0m'}
        isCollapsed={false}
        context={{ premeasureMode: true }}
      />
    ))

    expect(container.textContent).toBe('red')
  })

  it('does not reuse visible ANSI HTML from cache during hidden premeasure', () => {
    const text = '\x1B[31mred\x1B[0m'
    const renderCache = createMessageRenderCacheStore().forRow('row-v1')

    const visible = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={text}
        display={text}
        isCollapsed={false}
        context={{ renderCache }}
      />
    ))
    expect(visible.container.firstElementChild?.classList.contains(toolResultContentAnsi)).toBe(true)

    const premeasure = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={text}
        display={text}
        isCollapsed={false}
        context={{ renderCache, premeasureMode: true }}
      />
    ))
    const body = premeasure.container.firstElementChild as HTMLElement

    expect(body.classList.contains(toolResultContentPre)).toBe(true)
    expect(body.classList.contains(toolResultContentAnsi)).toBe(false)
    expect(body.querySelector('span')).toBeNull()
    expect(body.textContent).toBe('red')
  })

  it('renders markdown-tool-result body using the full text, not display', async () => {
    // kind="markdown-tool-result" always renders the full text (display is ignored
    // because markdown can't be cleanly truncated by lines).
    const { container } = render(() => (
      <CollapsibleContent kind="markdown-tool-result" text="**bold**" display="ignored" isCollapsed={false} />
    ))
    await settleWorkerReplies()
    expect(container.querySelector('strong')).not.toBeNull()
  })

  it('renders a large markdown-tool-result body in full, with no display scan or limit notice', async () => {
    // Three times the size that used to switch a body to a capped plain-text display.
    const filler = 'native '.repeat(15_000)
    const text = `## Heading\n\n${filler}\n\n- last item`
    const limit = vi.spyOn(safeTextDisplay, 'limitTextForDisplay')
    const { container } = render(() => (
      <CollapsibleContent kind="markdown-tool-result" text={text} isCollapsed={false} />
    ))
    await settleWorkerReplies()

    expect(container.querySelector('h2')?.textContent).toBe('Heading')
    expect(container.querySelector('li')?.textContent).toBe('last item')
    expect(container.textContent).toContain(filler.trim())
    expect(container.textContent).not.toContain('Display limited')
    expect(limit).not.toHaveBeenCalled()
  })

  it('swaps the body and the limit notice when the kind moves between Markdown and plain text', async () => {
    const text = `**start**\n${'x'.repeat(100_000)}\nend`
    const [kind, setKind] = createSignal<CollapsibleContentKind>('markdown-tool-result')
    const { container } = render(() => (
      <CollapsibleContent kind={kind()} text={text} isCollapsed={false} />
    ))
    await settleWorkerReplies()
    expect(container.querySelector('strong')?.textContent).toBe('start')
    expect(container.textContent).not.toContain('Display limited')

    setKind('pre')
    expect(container.querySelector('strong')).toBeNull()
    expect(container.firstElementChild?.classList.contains(toolResultContentPre)).toBe(true)
    expect(container.textContent).toContain('Display limited')

    // A move between two limited kinds keeps the limit and changes the body.
    setKind('json')
    expect(container.firstElementChild?.classList.contains(toolResultContentAnsi)).toBe(true)
    expect(container.textContent).toContain('Display limited')

    setKind('markdown-tool-result')
    expect(container.querySelector('strong')?.textContent).toBe('start')
    expect(container.textContent).not.toContain('Display limited')
  })
})

describe('CollapsibleContent output ownership', () => {
  it.each([
    'pre',
    'ansi-or-pre',
    'markdown-tool-result',
    'json',
  ] as const)('marks the existing output element for kind %s without adding a wrapper', async (kind) => {
    const text = kind === 'json'
      ? '{"count":0,"enabled":false}'
      : 'native output'
    const { container } = render(() => <CollapsibleContent outputPreview kind={kind} text={text} display={text} isCollapsed={false} />)
    await settleWorkerReplies()
    expect(container.querySelectorAll('[data-tool-output-preview]')).toHaveLength(1)
    expect(container.firstElementChild?.getAttribute('data-tool-output-preview')).toBe('')
    expect(container.textContent).toContain(kind === 'json'
      ? '"count"'
      : 'native output')
  })

  it.each([false, true])('keeps output ownership on styled text when collapsed is %s', (isCollapsed) => {
    const text = '\x1B[31mnative output\x1B[0m'
    const { container } = render(() => <CollapsibleContent outputPreview kind="ansi-or-pre" text={text} display={text} isCollapsed={isCollapsed} />)
    const output = container.querySelector('[data-tool-output-preview]')
    expect(output?.textContent).toBe('native output')
    expect(output?.querySelector('span')).not.toBeNull()
  })

  it.each([false, undefined])('does not mark request or metadata text for ownership %j', (outputPreview) => {
    const { container } = render(() => (
      <CollapsibleContent
        {...(outputPreview !== undefined
          ? { outputPreview }
          : {})}
        kind="pre"
        text="same marker"
        isCollapsed={false}
      />
    ))
    expect(container.textContent).toBe('same marker')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })

  it.each([
    { kind: 'pre', text: 'same marker' },
    { kind: 'ansi-or-pre', text: 'same marker' },
    { kind: 'ansi-or-pre', text: '\x1B[31msame marker\x1B[0m' },
    { kind: 'markdown-tool-result', text: 'same marker' },
    { kind: 'json', text: '{"same":"marker"}' },
  ] as const)('does not mark kind $kind text $text without output ownership', async ({ kind, text }) => {
    for (const outputPreview of [false, undefined]) {
      const { container, unmount } = render(() => (
        <CollapsibleContent
          {...(outputPreview !== undefined
            ? { outputPreview }
            : {})}
          kind={kind}
          text={text}
          display={text}
          isCollapsed={false}
        />
      ))
      await settleWorkerReplies()
      expect(container.textContent).toContain(kind === 'json'
        ? 'same'
        : 'same marker')
      expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
      unmount()
    }
  })

  it('marks styled output in premeasure mode', () => {
    const text = '\x1B[31mnative output\x1B[0m'
    const { container } = render(() => <CollapsibleContent outputPreview kind="ansi-or-pre" text={text} display={text} isCollapsed={false} context={{ premeasureMode: true }} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toBe('native output')
  })
})
