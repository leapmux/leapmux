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
    // Each copy commits in a frame of its own here: the test setup runs a frame callback
    // inline, so the report of one copy runs the shared premeasure frame before the loop
    // reaches the next copy. In the app, one frame reads the copies of a band together.
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

/** The client height that reportChatListSize gave each scroll container last. */
const reportedClientHeights = new WeakMap<HTMLElement, number>()

/**
 * Give `scrollContainer` a client height of `height`, as a pane with a layout box has.
 * jsdom computes no layout, so the height would read as 0, and the scroll hook counts a
 * pane of that height as hidden: it shows no place, and ChatView admits no row for a
 * target (see ViewportRestore.paneHidden).
 *
 * A container whose test defined a `clientHeight` of its own keeps that one.
 */
function giveClientHeight(scrollContainer: HTMLElement, height: number): void {
  const givenBefore = reportedClientHeights.has(scrollContainer)
  if (!givenBefore && Object.hasOwn(scrollContainer, 'clientHeight'))
    return
  reportedClientHeights.set(scrollContainer, height)
  if (givenBefore)
    return
  Object.defineProperty(scrollContainer, 'clientHeight', {
    configurable: true,
    get: () => reportedClientHeights.get(scrollContainer),
  })
}

/**
 * Report the size of the scroll container of `view` to ChatView through its
 * ResizeObserver, and return the container. The container gets the client height of that
 * size (see giveClientHeight), so the scroll hook reads the pane as shown in the frame of
 * the report. It leaves the hidden copies unmeasured, so no row joins the list yet (see
 * reportChatViewportSize).
 */
export async function reportChatListSize(view: { container: HTMLElement }, width = 800, height = 733): Promise<HTMLElement> {
  const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement | null
  expect(scrollContainer, 'The view has no scroll container').not.toBeNull()
  vi.spyOn(scrollContainer!, 'getBoundingClientRect').mockImplementation(() => ({
    width,
    height,
  }) as DOMRect)
  giveClientHeight(scrollContainer!, height)
  await triggerResizeObserverFor(scrollContainer!)
  return scrollContainer!
}

/**
 * Report the size of the scroll container of `view` to ChatView (see reportChatListSize),
 * and then measure the hidden copies (see measureHiddenCopies), so the rows of the first
 * view join the list.
 *
 * It measures nothing after a change of the width: ChatView renders no hidden copy until
 * the new width held for STAGING_WIDTH_SETTLE_MS. A test that needs the copies of a new
 * width waits for that time first, and then calls measureHiddenCopies.
 */
export async function reportChatViewportSize(view: { container: HTMLElement }, width = 800, height = 733): Promise<void> {
  await reportChatListSize(view, width, height)
  await measureHiddenCopies(view.container)
}
