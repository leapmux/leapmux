import { createEffect, createSignal, onCleanup } from 'solid-js'
import { createRafResizeObserver } from '~/lib/resizeObserver'

// ---------------------------------------------------------------------------
// Header title overflow
//
// A tool row's header is ONE line while the row is collapsed: its title box has the
// height of one line and clips what does not fit (see `toolUseTitle`). A request row
// draws its header from the newest call of its span, so its title can gain words
// when the result lands. The fixed height is what keeps that change from moving the
// row: the title is the one part of a request row that reads its result, and it
// cannot change the height of the row.
//
// The reader still needs the clipped words. This reports whether the collapsed title
// clips anything, and the row then offers Expand, which wraps the title.
// ---------------------------------------------------------------------------

/**
 * Whether the collapsed title in `box` clips anything: a part wider than its own box,
 * which ends in an ellipsis, or a part that broke onto a second line, which the box
 * cuts off at the height of one line.
 */
export function headerTitleClips(box: HTMLElement): boolean {
  if (box.scrollWidth > box.clientWidth + 0.5 || box.scrollHeight > box.clientHeight + 0.5)
    return true
  for (const part of box.querySelectorAll<HTMLElement>('*')) {
    // An inline box has no client width of its own, and no clip either.
    if (part.clientWidth > 0 && part.scrollWidth > part.clientWidth + 0.5)
      return true
  }
  return false
}

/**
 * Track whether the collapsed title clips anything.
 *
 * It measures the COLLAPSED title only. An expanded title clips nothing, and
 * reporting `false` then would remove the fact that made the row expandable, so the
 * control would disappear while the row stays expanded. It keeps the last collapsed
 * answer until the row collapses again.
 *
 * It measures again when the box resizes, and when the words of the title change in
 * place: a request row's title gains the words of its result without a resize.
 */
export function useHeaderTitleOverflow(props: {
  expanded: () => boolean
  onOverflowChange?: ((overflowing: boolean) => void) | undefined
}): {
  overflowing: () => boolean
  ref: (element: HTMLElement) => void
} {
  const [box, setBox] = createSignal<HTMLElement>()
  const [overflowing, setOverflowing] = createSignal(false)
  // The functional setter reads the previous value without tracking it, so the effect
  // that measures does not depend on the signal that it writes.
  const report = (next: boolean): void => {
    setOverflowing((previous) => {
      if (previous !== next)
        props.onOverflowChange?.(next)
      return next
    })
  }
  const measure = (): void => {
    const element = box()
    if (element === undefined || props.expanded())
      return
    report(headerTitleClips(element))
  }
  createEffect(() => {
    const element = box()
    if (element === undefined || props.expanded())
      return
    measure()
    const resize = createRafResizeObserver(() => measure())
    resize?.observe(element)
    const mutation = typeof MutationObserver === 'undefined'
      ? undefined
      : new MutationObserver(() => measure())
    mutation?.observe(element, { childList: true, subtree: true, characterData: true })
    onCleanup(() => {
      resize?.disconnect()
      mutation?.disconnect()
    })
  })
  return { overflowing, ref: setBox }
}
