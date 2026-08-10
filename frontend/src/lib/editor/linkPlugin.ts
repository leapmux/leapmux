import type { EditorState } from '@milkdown/prose/state'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { $prose } from '@milkdown/utils'

/** A contiguous run of text carrying one link mark, and that mark's href. */
export interface LinkRange {
  from: number
  to: number
  href: string
}

/**
 * The link run containing `pos`, or null when `pos` is not inside one.
 *
 * The run is widened across every adjacent text node that carries the SAME mark
 * instance, because ProseMirror splits a marked run at every other mark
 * boundary: `[**bold** rest](url)` is two text nodes under one link. Editing or
 * removing only the clicked node would leave half the link behind.
 */
export function linkRangeAt(state: EditorState, pos: number): LinkRange | null {
  const linkType = state.schema.marks.link
  if (!linkType)
    return null

  const $pos = state.doc.resolve(pos)
  const mark = linkType.isInSet($pos.marks())
  if (!mark)
    return null

  // Collect every text node carrying this exact mark, then merge outward from
  // the one holding the click while the pieces still abut. Two passes, because
  // `nodesBetween` runs forward: growing the range in that single pass would
  // never reach a piece that sits BEFORE the click.
  //
  // `isInSet` matches on type AND attrs, so an adjacent link with a different
  // href is a different mark and correctly stops the walk.
  const runs: { from: number, to: number }[] = []
  state.doc.nodesBetween($pos.start(), $pos.end(), (node, nodePos) => {
    if (node.isText && mark.isInSet(node.marks))
      runs.push({ from: nodePos, to: nodePos + node.nodeSize })
  })

  const index = runs.findIndex(run => run.from <= pos && pos <= run.to)
  if (index < 0)
    return null

  let { from, to } = runs[index]!
  for (let i = index - 1; i >= 0 && runs[i]!.to === from; i--)
    from = runs[i]!.from
  for (let i = index + 1; i < runs.length && runs[i]!.from === to; i++)
    to = runs[i]!.to

  return { from, to, href: String(mark.attrs.href ?? '') }
}

/** What the link popover needs from a click on a link. */
export interface LinkClickHandlers {
  /** The clicked run, or null when the click was not on a link. */
  setLinkRange: (range: LinkRange | null) => void
  setLinkPopoverOpen: (open: boolean) => void
  getLinkPopoverOpen: () => boolean
  getLinkRange: () => LinkRange | null
}

/**
 * Open an edit popover when the user clicks a link.
 *
 * This is the ONLY way to change or remove a link's URL. The markdown input rule
 * creates links, but nothing else can unmake one: editing the visible text in
 * place keeps the old href (the link mark is inclusive, so ProseMirror's
 * `marksAcross` re-applies it even across a delete-and-retype), so a corrected
 * label would silently ship the wrong URL to the agent.
 *
 * The click is NOT consumed — returning false lets ProseMirror place the caret
 * as usual, so clicking a link still behaves like clicking text.
 */
export function createLinkClickPlugin(handlers: LinkClickHandlers) {
  // Whether the popover was open for the run under the pointer, captured on
  // POINTERDOWN. A `popover="auto"` light-dismisses on pointerdown, so by the
  // time `handleClick` runs the popover has already closed and the open state
  // can no longer tell a toggle-closed from a fresh open. The code-language
  // label captures the same flag at the same moment, for the same reason.
  let wasOpenForRange = false

  return $prose(() => new Plugin({
    key: new PluginKey('link-click'),
    props: {
      handleDOMEvents: {
        pointerdown(view, event) {
          const at = view.posAtCoords({ left: event.clientX, top: event.clientY })
          const range = at ? linkRangeAt(view.state, at.pos) : null
          const current = handlers.getLinkRange()
          wasOpenForRange = !!range
            && handlers.getLinkPopoverOpen()
            && current?.from === range.from
            && current?.to === range.to
          return false
        },
      },
      handleClick(view, pos) {
        const range = linkRangeAt(view.state, pos)
        if (!range || wasOpenForRange) {
          handlers.setLinkPopoverOpen(false)
          handlers.setLinkRange(null)
          return false
        }
        handlers.setLinkRange(range)
        // Next frame, so `showPopover()` runs after this click has finished
        // propagating. Opening a `popover="auto"` from inside the click that is
        // still travelling means the browser's own light-dismiss pass sees a
        // pointerdown that landed outside the (now open) popover and closes it
        // again in the same gesture.
        requestAnimationFrame(() => handlers.setLinkPopoverOpen(true))
        return false
      },
    },
  }))
}
