import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ReadResultView } from './ReadResultView'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn().mockResolvedValue([[{ content: 'const x = 1', htmlStyle: { color: 'rgb(1, 2, 3)' } }]]),
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

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1')
  })

  it('enqueues tokenization when syntax highlighting is not paused', async () => {
    const { tokenizeAsync } = await import('~/lib/shikiWorkerClient')

    render(() => (
      <ReadResultView
        lines={[{ num: 1, text: 'const x = 1' }]}
        filePath="example.ts"
      />
    ))

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1')
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
      expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1')
    })
    await Promise.resolve()

    expect(container.textContent).toContain('const x = 1')
    expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).toBeNull()
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
      expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).not.toBeNull()
    })

    setPaused(true)

    await waitFor(() => {
      expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).not.toBeNull()
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
      expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).not.toBeNull()
    })

    setSelectionActive(true)

    await waitFor(() => {
      expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).not.toBeNull()
    })
  })

  it('does not apply in-flight tokenization while syntax highlighting becomes paused', async () => {
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

    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const x = 1')

    setPaused(true)
    resolveTokens?.([[{ content: 'const x = 1', htmlStyle: { color: 'rgb(9, 8, 7)' } }]])
    await Promise.resolve()

    expect(container.querySelector('[style*="rgb(9, 8, 7)"]')).toBeNull()

    setPaused(false)

    await waitFor(() => {
      expect(tokenizeAsync).toHaveBeenCalledTimes(2)
      expect(container.querySelector('[style*="rgb(1, 2, 3)"]')).not.toBeNull()
    })
  })
})
