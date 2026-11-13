import type { MarkdownRenderRequest } from './markdownWorker'
import type { CapturingWorker } from '~/test-support/fakeWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { installCapturingWorkers } from '~/test-support/fakeWorker'

/**
 * Any pair, made current in each fresh module (see importClient). These tests are
 * about the transport, not about the colours. The client posts no render whose pair
 * is not current.
 */
const PAIR = { light: 'github-light', dark: 'github-dark' }

/** Import a fresh client, and make PAIR the current pair of its fresh theme module. */
async function importClient() {
  vi.resetModules()
  const client = await import('./markdownWorkerClient')
  const { setSyntaxThemePair } = await import('./shikiThemes')
  setSyntaxThemePair(PAIR)
  return { ...client, setSyntaxThemePair }
}

// Captured-worker/message lookup: each test pushes exactly the entries it
// expects, so a missing entry is a harness failure, not a case to guard.
function captured<T>(list: readonly T[], index: number): T {
  const value = list[index]
  if (value === undefined)
    throw new Error(`expected a captured entry at index ${index}`)
  return value
}

/** Answer the `index`th message that `worker` received with a clean render of `html`. */
function answer(worker: CapturingWorker<MarkdownRenderRequest>, index: number, html: string, styles: Record<string, string> = {}): void {
  worker.reply({
    id: captured(worker.messages, index).id,
    html,
    retryable: false,
    styles,
  })
}

describe('markdownWorkerClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns null where no Worker exists, as for a worker that cannot start', async () => {
    vi.stubGlobal('Worker', undefined)
    const { renderMarkdownInWorker } = await importClient()

    await expect(renderMarkdownInWorker('no worker', PAIR)).resolves.toBeNull()
  })

  it('returns null when creating the worker throws synchronously', async () => {
    vi.stubGlobal('Worker', class ThrowingWorker {
      constructor() {
        throw new Error('blocked by CSP')
      }
    })
    const { renderMarkdownInWorker } = await importClient()

    await expect(renderMarkdownInWorker('no worker', PAIR)).resolves.toBeNull()
  })

  it('resolves a render whose message cannot be posted to null, and keeps the worker for the others', async () => {
    const workers = installCapturingWorkers<MarkdownRenderRequest>()
    const { renderMarkdownInWorker } = await importClient()
    const first = renderMarkdownInWorker('first', PAIR)
    const worker = captured(workers, 0)
    worker.postMessage = () => {
      throw new DOMException('the message cannot be cloned', 'DataCloneError')
    }

    await expect(renderMarkdownInWorker('second', PAIR)).resolves.toBeNull()

    expect(worker.terminate).not.toHaveBeenCalled()
    answer(worker, 0, '<p>first</p>')
    await expect(first).resolves.toEqual({ html: '<p>first</p>', retryable: false, styles: {} })
  })

  it('ignores a stale error event from a replaced worker', async () => {
    const workers = installCapturingWorkers<MarkdownRenderRequest>()
    const { renderMarkdownInWorker } = await importClient()

    const first = renderMarkdownInWorker('first', PAIR)
    captured(workers, 0).onerror?.()
    await expect(first).resolves.toBeNull()

    const second = renderMarkdownInWorker('second', PAIR)
    let settled = false
    second.then(() => {
      settled = true
    })
    captured(workers, 0).onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    answer(captured(workers, 1), 0, '<p>second</p>', { 'sk-x-1': '--shiki-light:#abc' })
    await expect(second).resolves.toEqual({
      html: '<p>second</p>',
      retryable: false,
      styles: { 'sk-x-1': '--shiki-light:#abc' },
    })
  })

  describe('a render held while the theme changes', () => {
    const otherPair = { light: 'nord-light', dark: 'nord' }

    // The gate holds all but `maxInFlight` (2) jobs client-side, so the third
    // render reaches the worker after an arbitrary delay. A render of the theme
    // the user left could only be discarded, and it would delay the renders
    // behind it by the whole of its own.
    it('posts no render whose theme moved while the gate held it, and resolves it to null', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { renderMarkdownInWorker, setSyntaxThemePair } = await importClient()
      const renders = [
        renderMarkdownInWorker('a', PAIR),
        renderMarkdownInWorker('b', PAIR),
        renderMarkdownInWorker('c', PAIR),
      ]
      const worker = captured(workers, 0)
      expect(worker.messages).toHaveLength(2)

      // The user picks another theme while 'c' waits; then a slot frees.
      setSyntaxThemePair(otherPair)
      answer(worker, 0, '<p>a</p>')
      await captured(renders, 0)

      expect(worker.messages.map(m => m.text)).toEqual(['a', 'b'])
      await expect(captured(renders, 2)).resolves.toBeNull()
    })

    // A dropped render must give its slot back, or each theme change would take a
    // slot of the gate for good, and the lane would stop after two of them.
    it('frees the slot of a render that it drops, so a render of the new theme reaches the worker at once', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { renderMarkdownInWorker, setSyntaxThemePair } = await importClient()
      const renders = [
        renderMarkdownInWorker('a', PAIR),
        renderMarkdownInWorker('b', PAIR),
        renderMarkdownInWorker('c', PAIR),
      ]
      const worker = captured(workers, 0)
      setSyntaxThemePair(otherPair)
      answer(worker, 0, '<p>a</p>')
      await captured(renders, 0)
      await expect(captured(renders, 2)).resolves.toBeNull()

      // 'b' still holds one slot, and the dropped 'c' holds none.
      const next = renderMarkdownInWorker('d', otherPair)

      expect(worker.messages.map(m => m.text)).toEqual(['a', 'b', 'd'])
      expect(captured(worker.messages, 2).syntax).toBe(otherPair)
      answer(worker, 2, '<p>d</p>')
      await expect(next).resolves.toEqual({ html: '<p>d</p>', retryable: false, styles: {} })
    })

    // The message is built from the pair that the caller captured with the
    // artifact namespace. A pair read when the gate releases the job could differ
    // from it, and the reply would then be filed under one theme while it carries
    // another theme's colours.
    it('posts a held render with its own pair when the theme changed and came back', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { renderMarkdownInWorker, setSyntaxThemePair } = await importClient()
      const renders = [
        renderMarkdownInWorker('a', PAIR),
        renderMarkdownInWorker('b', PAIR),
        renderMarkdownInWorker('c', PAIR),
      ]
      const worker = captured(workers, 0)

      setSyntaxThemePair(otherPair)
      setSyntaxThemePair({ ...PAIR })
      answer(worker, 0, '<p>a</p>')
      await captured(renders, 0)

      // The live pair is an equal copy, so only the captured object passes `toBe`.
      const posted = worker.messages.find(m => m.text === 'c')
      expect(posted?.syntax).toBe(PAIR)
    })
  })

  describe('render time budget', () => {
    it('gives every render the base budget plus a share for each KB of source', async () => {
      const { MARKDOWN_RENDER_BASE_BUDGET_MS, MARKDOWN_RENDER_BUDGET_MS_PER_KB, markdownRenderBudgetMs } = await importClient()

      expect(markdownRenderBudgetMs('')).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS)
      expect(markdownRenderBudgetMs('x')).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + MARKDOWN_RENDER_BUDGET_MS_PER_KB)
      expect(markdownRenderBudgetMs('x'.repeat(1024))).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + MARKDOWN_RENDER_BUDGET_MS_PER_KB)
      expect(markdownRenderBudgetMs('x'.repeat(1024 * 1024))).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + 1024 * MARKDOWN_RENDER_BUDGET_MS_PER_KB)
    })

    it('resolves a render that runs past its budget to null, and renders the body behind it on a fresh worker', async () => {
      vi.useFakeTimers()
      try {
        const workers = installCapturingWorkers<MarkdownRenderRequest>()
        const { markdownRenderBudgetMs, renderMarkdownInWorker } = await importClient()
        const nested = '*a '.repeat(2_000)
        // A worker that answered once has started, so its budget holds no
        // allowance for a cold start (see createWorkerClient).
        const warm = renderMarkdownInWorker('warm', PAIR)
        answer(captured(workers, 0), 0, '<p>warm</p>')
        await warm

        const stuck = renderMarkdownInWorker(nested, PAIR)
        const behind = renderMarkdownInWorker('behind', PAIR)
        vi.advanceTimersByTime(markdownRenderBudgetMs(nested))

        await expect(stuck).resolves.toBeNull()
        expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
        const fresh = captured(workers, 1)
        expect(fresh.messages.map(m => m.text)).toEqual(['behind'])
        answer(fresh, 0, '<p>behind</p>')
        await expect(behind).resolves.toEqual({ html: '<p>behind</p>', retryable: false, styles: {} })
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('render lanes', () => {
    it('renders a body at the size limit in the standard lane, and a larger one in a worker of its own', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { LARGE_MARKDOWN_BODY_CHARS, renderMarkdownInWorker } = await importClient()
      const atLimit = 'a'.repeat(LARGE_MARKDOWN_BODY_CHARS)
      const overLimit = 'b'.repeat(LARGE_MARKDOWN_BODY_CHARS + 1)

      void renderMarkdownInWorker(atLimit, PAIR)
      expect(workers).toHaveLength(1)
      void renderMarkdownInWorker(overLimit, PAIR)

      expect(workers).toHaveLength(2)
      expect(captured(workers, 0).messages.map(m => m.text)).toEqual([atLimit])
      expect(captured(workers, 1).messages.map(m => m.text)).toEqual([overLimit])
    })

    it('spawns no large-lane worker until a large body arrives', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { renderMarkdownInWorker } = await importClient()

      void renderMarkdownInWorker('small one', PAIR)
      void renderMarkdownInWorker('small two', PAIR)
      void renderMarkdownInWorker('small three', PAIR)

      expect(workers).toHaveLength(1)
    })

    it('posts a small body at once while large bodies fill their own lane', async () => {
      const workers = installCapturingWorkers<MarkdownRenderRequest>()
      const { LARGE_MARKDOWN_BODY_CHARS, renderMarkdownInWorker } = await importClient()
      const large = (n: number) => `${n}`.padEnd(LARGE_MARKDOWN_BODY_CHARS + 1, 'x')

      // More large bodies than the large lane's gate posts at once: the rest wait.
      let largeSettled = 0
      for (const n of [1, 2, 3]) {
        void renderMarkdownInWorker(large(n), PAIR).then(() => {
          largeSettled++
        })
      }
      const small = renderMarkdownInWorker('small body', PAIR)

      const largeWorker = captured(workers, 0)
      const smallWorker = captured(workers, 1)
      expect(largeWorker.messages).toHaveLength(2)
      expect(smallWorker.messages.map(m => m.text)).toEqual(['small body'])

      // The small body completes while every large body is still pending.
      answer(smallWorker, 0, '<p>small body</p>')
      await expect(small).resolves.toEqual({ html: '<p>small body</p>', retryable: false, styles: {} })
      expect(largeSettled).toBe(0)
    })
  })

  it('defaults a missing styles dictionary to empty (older worker response shape)', async () => {
    const workers = installCapturingWorkers<MarkdownRenderRequest>()
    const { renderMarkdownInWorker } = await importClient()

    const pending = renderMarkdownInWorker('text', PAIR)
    const { id } = captured(captured(workers, 0).messages, 0)
    captured(workers, 0).reply({ id, html: '<p>text</p>', retryable: false })
    await expect(pending).resolves.toEqual({ html: '<p>text</p>', retryable: false, styles: {} })
  })
})
