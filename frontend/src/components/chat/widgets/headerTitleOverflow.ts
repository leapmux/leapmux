import { onCleanup } from 'solid-js'
import { useCollapsedOverflow } from './collapsedOverflow'

// ---------------------------------------------------------------------------
// Header title overflow
//
// A tool row's header is ONE line while the row is collapsed: its title box has the
// height of one line and clips what does not fit (see `toolUseTitle`). A request row
// draws its header -- the icon, the tool name and the title -- from the newest call of
// its span, so its title can gain words when the result lands. The fixed height is
// what keeps that change from moving the collapsed row: the header is the one part of
// a request row that reads its result. An expanded row wraps its title, so its height
// then follows the words (see ChatView's height key of an expanded header).
//
// The reader still needs the clipped words. This reports whether the collapsed title
// clips anything, and the row then offers Expand, which wraps the title.
// ---------------------------------------------------------------------------

/**
 * Whether the collapsed title in `box` clips anything: a part wider than its own box,
 * which ends in an ellipsis, or a part that broke onto a second line, which the box
 * cuts off at the height of one line.
 *
 * The height takes one pixel of slack. A title can set a proportional and a monospace
 * face on one baseline, and the browser rounds the content height of that line, so a
 * title that fits can read one pixel taller than its box. A second line adds a whole
 * line of height.
 */
export function headerTitleClips(box: HTMLElement): boolean {
  if (box.scrollWidth > box.clientWidth + 0.5 || box.scrollHeight > box.clientHeight + 1)
    return true
  for (const part of box.querySelectorAll<HTMLElement>('*')) {
    // An inline box has no client width of its own, and no clip either.
    if (part.clientWidth > 0 && part.scrollWidth > part.clientWidth + 0.5)
      return true
  }
  return false
}

/**
 * Track whether the collapsed title clips anything (see useCollapsedOverflow for the
 * rules that it shares with the command summary).
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
  return useCollapsedOverflow({
    collapsed: () => !props.expanded(),
    clips: headerTitleClips,
    watch: (element, measure) => {
      if (typeof MutationObserver === 'undefined')
        return
      const mutation = new MutationObserver(() => measure())
      mutation.observe(element, { childList: true, subtree: true, characterData: true })
      onCleanup(() => mutation.disconnect())
    },
    onOverflowChange: next => props.onOverflowChange?.(next),
  })
}
