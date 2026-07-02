import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ReadResultView } from './ReadResultView'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn().mockResolvedValue([[{ content: 'const x = 1', className: 'sk-read-test' }]]),
}))

describe('readresultview syntax highlighting', () => {
  beforeEach(() => {
    vi.clearAllMocks()
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
    const rules = document.querySelector('style[data-shiki-style-classes]')!.textContent!
    expect(rules).toContain(`.${tokenSpan!.className}{`)
    expect(rules).toContain('--shiki-light')
  })
})
