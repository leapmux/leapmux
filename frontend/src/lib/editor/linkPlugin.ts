import type { EditorState } from '@milkdown/prose/state'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { $prose } from '@milkdown/utils'

/** A contiguous run of text carrying one link mark, and that mark's attributes. */
export interface LinkRange {
  from: number
  to: number
  href: string
  /**
   * The clicked mark's full attribute set. The schema declares `title` beside
   * `href`, and a pasted `[docs](url "The docs")` carries one, so an edit that
   * rebuilt the mark from `href` alone would drop the title with nothing said.
   */
  attrs: Record<string, unknown>
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
  // Resolve the mark from the node the position is INSIDE, not from
  // `$pos.marks()`. At a text-node boundary (textOffset 0) `marks()` reports the
  // marks of the node BEFORE the position, so a click on the first character of
  // a link that follows plain text found no link and the popover refused to
  // open -- and, at the end of a trailing link, it found one for a click in the
  // empty space past it. `nodeAfter` is the containing node's remainder when the
  // position is mid-node and the next node at a boundary, so one expression
  // answers both, and it is null at the block end where there is no link.
  const mark = $pos.nodeAfter ? linkType.isInSet($pos.nodeAfter.marks) : null
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

  return { from, to, href: String(mark.attrs.href ?? ''), attrs: mark.attrs }
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
 * What Mod-K acts on, or null when it should do nothing.
 *
 * Two cases, and the SELECTION decides which:
 *
 *  - A non-empty selection is a request to link THAT text, so the range is the
 *    selection and the href starts EMPTY. Applying then overrides whatever link
 *    marks the selection already spans -- `applyLinkHref` clears the range
 *    before it marks it -- so selecting across a link and giving a new URL
 *    replaces it rather than leaving two.
 *  - A bare caret inside a link is a request to edit THAT link, so the range is
 *    the whole run and the href is its current one. The run, not the containing
 *    text node: another mark splits one link into several nodes.
 *
 * A caret outside a link has nothing to link and nothing to edit, so Mod-K falls
 * through untouched. Split out of the plugin because this decision is the whole
 * behaviour, and a ProseMirror keydown handler is not reachable from a unit test.
 */
export function linkShortcutTarget(state: EditorState): LinkRange | null {
  if (!state.schema.marks.link)
    return null

  const { from, to, empty } = state.selection
  // The caret case: edit the link it sits in, if any.
  if (empty)
    return linkRangeAt(state, from)

  // The selection case: link the selected text, starting from an EMPTY href. A
  // selection that spans a block boundary cannot carry one mark, so it is left
  // alone rather than silently linking part of it.
  if (!state.doc.resolve(from).sameParent(state.doc.resolve(to)))
    return null
  return { from, to, href: '', attrs: {} }
}

export function createLinkShortcutPlugin(handlers: LinkClickHandlers) {
  return $prose(() => new Plugin({
    key: new PluginKey('link-shortcut'),
    props: {
      handleKeyDown(view, event) {
        const isModK = (event.key === 'k' || event.key === 'K')
          && (event.metaKey || event.ctrlKey)
          && !event.shiftKey && !event.altKey
        if (!isModK)
          return false

        const range = linkShortcutTarget(view.state)
        if (!range)
          return false

        event.preventDefault()
        handlers.setLinkRange(range)
        // Next frame, for the same reason the click path defers: opening a
        // `popover="auto"` from inside the event that is still propagating lets
        // the browser's own dismiss pass close it again in the same gesture, so
        // the panel never appears. Deferring puts `showPopover()` after this
        // keydown has finished.
        requestAnimationFrame(() => handlers.setLinkPopoverOpen(true))
        return true
      },
    },
  }))
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
