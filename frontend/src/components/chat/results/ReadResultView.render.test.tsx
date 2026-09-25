import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readInjectedShikiRules } from '~/lib/shikiStyleClass.testkit'
import { EXPANDED_TEXT_DISPLAY_CHAR_LIMIT, EXPANDED_TEXT_DISPLAY_LINE_LIMIT } from '../safeTextDisplay'
import { ReadResultView } from './ReadResultView'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn().mockResolvedValue([[{ content: 'const x = 1', className: 'sk-read-test' }]]),
}))

describe('ReadResultView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // A provider that leaves lines out of a read states the gap as a row with no number.
  it('draws an elision row with no number and sizes the gutter from the largest number', () => {
    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 9, text: 'function f() {' }, { num: null, text: '…' }, { num: 120, text: '}' }, { num: null, text: '…' }]}
        premeasureMode
      />
    ))
    // One child of the view for each line.
    const rows = [...container.firstElementChild!.children] as HTMLElement[]
    expect(rows.map(row => row.textContent)).toEqual(['9function f() {', '…', '120}', '…'])
    // A gap row carries no number, so a quote that starts on it states no line.
    expect(rows.map(row => row.dataset.lineNum ?? null)).toEqual(['9', null, '120', null])
    // The largest number sizes the gutter, not the last row, which is a gap here.
    const gutters = rows.map(row => (row.firstElementChild as HTMLElement).style.width)
    expect(gutters).toEqual(['3ch', '3ch', '3ch', '3ch'])
  })

  it('sizes the gutter to one column when no row has a number', () => {
    const { container } = render(() => (
      <ReadResultView lines={[{ num: null, text: '…' }, { num: null, text: '…' }]} premeasureMode />
    ))
    const rows = [...container.firstElementChild!.children] as HTMLElement[]
    expect(rows.map(row => row.dataset.lineNum ?? null)).toEqual([null, null])
    expect(rows.map(row => (row.firstElementChild as HTMLElement).style.width)).toEqual(['1ch', '1ch'])
  })

  it('keeps a line numbered zero, which is not an elision row', () => {
    // Zero is falsy, so a truthiness test in place of the null test would take the
    // number off the row and off the quote that starts on it.
    const { container } = render(() => (
      <ReadResultView lines={[{ num: 0, text: 'first' }, { num: null, text: '…' }]} premeasureMode />
    ))
    const rows = [...container.firstElementChild!.children] as HTMLElement[]
    expect(rows.map(row => row.dataset.lineNum ?? null)).toEqual(['0', null])
    expect(rows.map(row => row.textContent)).toEqual(['0first', '…'])
  })

  it('does not enqueue tokenization while visible scrolling has syntax highlighting paused', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        syntaxHighlightingPaused
      />
    ))

    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('does not enqueue tokenization while text selection is active', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        textSelectionActive={() => true}
      />
    ))

    expect(tokenizeAsync).not.toHaveBeenCalled()
  })

  it('enqueues tokenization after an initially active text selection clears', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const [selectionActive, setSelectionActive] = createSignal(true)

    render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        textSelectionActive={selectionActive}
      />
    ))

    expect(tokenizeAsync).not.toHaveBeenCalled()

    setSelectionActive(false)

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1', expect.any(Function))
  })

  it('enqueues tokenization when syntax highlighting is not paused', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
      />
    ))

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1', expect.any(Function))
  })

  it('falls back to plain text when worker tokenization returns null', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    vi.mocked(tokenizeAsync).mockResolvedValueOnce(null)

    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
      />
    ))

    await waitFor(() => {
      expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1', expect.any(Function))
    })
    await Promise.resolve()

    expect(container.textContent).toContain('const x = 1')
    expect(container.querySelector('.sk-read-test')).toBeNull()
  })

  it('limits one large file line before it reaches tokenization or the DOM', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    const text = `READ_HEAD${'x'.repeat(100_000)}READ_TAIL`
    const { container } = render(() => (
      <ReadResultView lines={[{ num: 1, text }]} filePath="example.ts" />
    ))

    expect(container.textContent!.length).toBeLessThan(text.length)
    expect(container.textContent).toContain('READ_HEAD')
    expect(container.textContent).toContain('READ_TAIL')
    expect(container.textContent).toContain('Display limited')
    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', expect.not.stringContaining('x'.repeat(10_000)), expect.any(Function))
  })

  it('limits the total rows of an expanded structured read', () => {
    const lines = Array.from({ length: EXPANDED_TEXT_DISPLAY_LINE_LIMIT + 500 }, (_, index) => ({
      num: index + 1,
      text: `line ${index + 1}`,
    }))
    const { container } = render(() => <ReadResultView lines={lines} />)

    expect(container.querySelectorAll('[data-line-num]').length).toBeLessThanOrEqual(EXPANDED_TEXT_DISPLAY_LINE_LIMIT)
    expect(container).toHaveTextContent('Display limited')
    expect(container).not.toHaveTextContent(`line ${lines.length}`)
  })

  it('limits total characters across individually short file lines', () => {
    const lineText = 'x'.repeat(100)
    const lines = Array.from({ length: 900 }, (_, index) => ({ num: index + 1, text: lineText }))
    const { container } = render(() => <ReadResultView lines={lines} />)

    expect((container.textContent ?? '').length).toBeLessThan(EXPANDED_TEXT_DISPLAY_CHAR_LIMIT + 5_000)
    expect(container).toHaveTextContent('Display limited')
    expect(container.querySelectorAll('[data-line-num]').length).toBeLessThan(lines.length)
  })

  it('keeps a final short line that fits the remaining character budget', () => {
    const lines = [
      ...Array.from({ length: 15 }, (_, index) => ({ num: index + 1, text: 'x'.repeat(4_096) })),
      { num: 16, text: 'y'.repeat(4_025) },
      { num: 17, text: 'z' },
    ]
    const { container } = render(() => <ReadResultView lines={lines} />)

    expect(container.querySelectorAll('[data-line-num]')).toHaveLength(lines.length)
    expect(container).toHaveTextContent('z')
  })

  it('keeps existing tokens when syntax highlighting is paused after highlight completes', async () => {
    const [paused, setPaused] = createSignal(false)
    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        syntaxHighlightingPaused={paused()}
      />
    ))

    await waitFor(() => {
      expect(container.querySelector('.sk-read-test')).not.toBeNull()
    })

    setPaused(true)

    await waitFor(() => {
      expect(container.querySelector('.sk-read-test')).not.toBeNull()
    })
  })

  it('keeps existing tokens while text selection is active after highlight completes', async () => {
    const [selectionActive, setSelectionActive] = createSignal(false)
    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        textSelectionActive={selectionActive}
      />
    ))

    await waitFor(() => {
      expect(container.querySelector('.sk-read-test')).not.toBeNull()
    })

    setSelectionActive(true)

    await waitFor(() => {
      expect(container.querySelector('.sk-read-test')).not.toBeNull()
    })
  })

  it('defers an in-flight tokenization that lands while paused, then applies it on resume (no re-dispatch)', async () => {
    // A worker tokenization dispatched while UNpaused that resolves AFTER a scroll-pause
    // came up is STASHED and applied once the pause lifts -- not discarded and recomputed.
    // (A pause re-runs the dispatch effect; the hook must keep the in-flight dispatch
    // live and stash its result rather than cancel + re-dispatch the same work.)
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')
    let resolveTokens: ((tokens: Awaited<ReturnType<typeof tokenizeAsync>>) => void) | undefined
    vi.mocked(tokenizeAsync).mockImplementationOnce(() => new Promise((resolve) => {
      resolveTokens = resolve
    }))
    const [paused, setPaused] = createSignal(false)
    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
        syntaxHighlightingPaused={paused()}
      />
    ))

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1', expect.any(Function))

    // Pause, then the in-flight worker resolves WHILE paused: stashed, not yet applied
    // (replacing text nodes mid-scroll is what the pause guards against).
    setPaused(true)
    resolveTokens?.([[{ content: 'const x = 1', className: 'sk-read-stash' }]])
    await Promise.resolve()

    expect(container.querySelector('.sk-read-stash')).toBeNull()

    // Resume: the STASHED result (sk-read-stash) is applied, with no second worker dispatch.
    setPaused(false)

    await waitFor(() => {
      expect(container.querySelector('.sk-read-stash')).not.toBeNull()
    })
    expect(tokenizeAsync).toHaveBeenCalledTimes(1)
  })

  it('marks token spans with data-shiki-token but not the line-number span', async () => {
    // The dual-theme color rule targets `span[data-shiki-token]`, not a bare `span[style]`:
    // the line-number span carries an inline `style` (its width) too, so a `span[style]`
    // rule would override its faint color with `var(--shiki-light)` (which resolves to
    // nothing on a non-token span). Assert the marker distinguishes the two.
    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
      />
    ))

    await waitFor(() => {
      expect(container.querySelector('[data-shiki-token]')).not.toBeNull()
    })

    // The colored token span carries the marker.
    const tokenSpan = container.querySelector('.sk-read-test')
    expect(tokenSpan).not.toBeNull()
    expect(tokenSpan!.hasAttribute('data-shiki-token')).toBe(true)

    // The line-number span (inline width style, but NOT a syntax token) must not, or the
    // color rule would strip its faint styling.
    const lineNumberSpan = [...container.querySelectorAll('span')].find(
      s => s.textContent === '1' && (s.getAttribute('style') ?? '').includes('width'),
    )
    expect(lineNumberSpan).toBeDefined()
    expect(lineNumberSpan!.hasAttribute('data-shiki-token')).toBe(false)
  })

  it('tokenizes a .log (ANSI) file synchronously on the main thread, never via the worker', async () => {
    // ANSI is a Shiki built-in the worker's Oniguruma core has no grammar for, so the
    // hook's syncTokenize path must handle it on the main thread (guessLanguage maps
    // `.log` -> `ansi`). The worker must NOT be dispatched, and the colored token spans
    // render synchronously.
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    const { container } = render(() => (
      <ReadResultView
        lines={[{ num: 1, text: '[31mred[0m plain' }]}
        filePath="server.log"
      />
    ))

    // No worker round-trip: ANSI tokenized synchronously, terminal.
    expect(tokenizeAsync).not.toHaveBeenCalled()
    // The visible payload is the ANSI-stripped text, split into themed token spans
    // whose shared style classes define the dual-theme CSS variables (proving
    // tokenization ran, not plain fallback — see shikiStyleClass).
    expect(container.textContent).toContain('red')
    const tokenSpan = container.querySelector('[data-shiki-token][class^="sk-"]')
    expect(tokenSpan).not.toBeNull()
    const rules = readInjectedShikiRules()
    expect(rules).toContain(`.${tokenSpan!.className}{`)
    expect(rules).toContain('--shiki-light')
  })
})
