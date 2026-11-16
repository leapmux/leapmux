import { expect, vi } from 'vitest'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { flushAnimationFrame, triggerResizeObserverFor, triggerResizeObserverForSync } from '~/test-support/resizeObserverStub'

// Helpers for a test that mounts the real ChatView.
//
// The chat list holds measured rows only: a loaded row joins the list after a hidden
// copy measured it (see chatRowAdmission). jsdom computes no layout, so a hidden copy
// reads a height of 0 by 0 and reports nothing, and no row joins. These helpers give
// each hidden copy a layout box, as the browser does.
//
// They need the controllable ResizeObserver of `~/test-support/resizeObserverStub`.

/** The most rounds of measureHiddenCopies: far more than the staging of a test needs. */
const MEASURE_ROUNDS_MAX = 20

/** The height (px) that measureHiddenCopies gives a hidden copy when the test states none. */
const DEFAULT_COPY_HEIGHT_PX = 96

/**
 * Measure each hidden copy of `container`: each child of a `[data-chat-premeasure-root]`
 * gets a layout box of 800 by `heightOf(copy)` pixels, and its ResizeObserver fires.
 *
 * It repeats while a root still holds a copy, at most MEASURE_ROUNDS_MAX times: a commit
 * admits rows, and the staging then mounts more copies. A copy reports no height while its
 * content waits for a worker (a Markdown body, a diff), so each round first waits for the
 * replies of the stand-in workers (see settleWorkerReplies).
 *
 * It throws when a copy remains after the last round. Such a copy never reports a height,
 * so its row never joins the list, and a test that went on would read the text of the
 * copy where it means the row.
 *
 * The default height is DEFAULT_COPY_HEIGHT_PX for each copy.
 */
export async function measureHiddenCopies(
  container: HTMLElement,
  heightOf: (copy: HTMLElement) => number = () => DEFAULT_COPY_HEIGHT_PX,
): Promise<void> {
  for (let round = 0; round < MEASURE_ROUNDS_MAX; round++) {
    await settleWorkerReplies()
    const copies = [
      ...container.querySelectorAll<HTMLElement>('[data-chat-premeasure-root] > *'),
    ]
    if (copies.length === 0)
      return
    // Every copy of the round in one frame, as the shared premeasure frame of the app reads
    // the copies of one band together.
    for (const copy of copies) {
      vi.spyOn(copy, 'getBoundingClientRect').mockImplementation(() => new DOMRect(0, 0, 800, heightOf(copy)))
      triggerResizeObserverForSync(copy)
    }
    await flushAnimationFrame()
  }
  const remaining = container.querySelectorAll('[data-chat-premeasure-root] > *').length
  if (remaining > 0)
    throw new Error(`${remaining} hidden copies report no height after ${MEASURE_ROUNDS_MAX} rounds, so their rows cannot join the list`)
}

/**
 * Report the size of the scroll container of `view` to ChatView through its
 * ResizeObserver, and then measure the hidden copies (see measureHiddenCopies), so the
 * rows of the first view join the list.
 */
export async function reportChatViewportSize(view: { container: HTMLElement }, width = 800, height = 733): Promise<void> {
  const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement | null
  expect(scrollContainer).not.toBeNull()
  vi.spyOn(scrollContainer!, 'getBoundingClientRect').mockImplementation(() => ({
    width,
    height,
  }) as DOMRect)
  await triggerResizeObserverFor(scrollContainer!)
  await measureHiddenCopies(view.container)
}
