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

/**
 * Whether a press on `range` is a request to CLOSE the popover rather than open
 * one, captured at pointerdown.
 *
 * A `popover="auto"` light-dismisses on pointerdown, so by the time the click is
 * handled the popover already closed and its open state can no longer tell a
 * toggle-closed from a fresh open. Only the state at pointerdown can.
 *
 * Pure, and exported, because it is half of the decision this plugin exists to
 * make -- the same reason `linkShortcutTarget` sits outside its plugin.
 */
export function pressClosesPopover(
  range: LinkRange | null,
  current: LinkRange | null,
  popoverOpen: boolean,
): boolean {
  return !!range && popoverOpen && current?.from === range.from && current?.to === range.to
}

/**
 * What a completed press on `range` should do to the popover.
 *
 * `selectionEmpty` is what keeps a SELECTION gesture from opening the editor.
 * ProseMirror's own `handleClick` never fired after a drag of more than 4 px or
 * a shift-click -- `MouseDown` sets `allowDefault` in both cases and `up()`
 * then skips `handleSingleClick`. Reading the raw `click` event buys the
 * reliability this plugin moved here for, and gives up that suppression, so the
 * rule has to be stated: dragging to select part of a link's text, or
 * shift-clicking to extend a selection onto one, popped the URL editor over the
 * text the user was selecting.
 *
 * The selection answers both gestures at once, and it answers them AFTER
 * ProseMirror has applied the gesture, so it needs no coordinate bookkeeping and
 * no `shiftKey` test: a plain click leaves a caret, and every gesture that
 * selects leaves a range.
 */
export function clickAction(
  range: LinkRange | null,
  pressClosed: boolean,
  selectionEmpty: boolean,
): 'open' | 'close' {
  if (!range || pressClosed)
    return 'close'
  return selectionEmpty ? 'open' : 'close'
}

/**
 * The intent of the press that is in flight, held between its pointerdown and
 * its click.
 *
 * A separate object because it is the part with a state machine in it, and
 * `createLinkClickPlugin` returns a `$prose` that no test can drive.
 *
 * The answer is captured on POINTERDOWN because the browser's light-dismiss
 * pass for `popover="auto"` runs there: by click time `getLinkPopoverOpen()`
 * already reads false for a press that dismissed the popover, so the click
 * cannot tell "the user pressed the link to shut the editor" from "the user
 * pressed the link to open it". The pointerdown can.
 *
 * That early capture is what makes the disarm rules load-bearing. A press must
 * hand its answer to ONE click and no other:
 *
 *  - `take` disarms as it reads, so a second click with no press between them
 *    cannot inherit the first one's intent.
 *  - `disarm` is for a press that ends with no click at all -- the browser
 *    takes the pointer for a scroll or a system gesture and sends
 *    `pointercancel`.
 *
 * A click that no press armed falls back to `live()`, and that is the correct
 * answer rather than a safe guess: no pointerdown ran, so no light-dismiss
 * pass ran either, so the popover state the click reads is still truthful.
 */
export function createArmedPress() {
  let armed: boolean | undefined
  return {
    /** Record what the press that just started should do to the popover. */
    arm(closesPopover: boolean) {
      armed = closesPopover
    },
    /** Drop the armed answer, for a press that will produce no click. */
    disarm() {
      armed = undefined
    },
    /** The armed answer, or `live()` when no press armed this click. Disarms. */
    take(live: () => boolean): boolean {
      const closesPopover = armed ?? live()
      armed = undefined
      return closesPopover
    },
  }
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
  // Whether the press that is in flight closes the popover rather than opening
  // one -- see `createArmedPress` for why the answer is captured early and what
  // disarms it.
  const press = createArmedPress()

  // BOTH halves are raw DOM handlers, and that is load-bearing. ProseMirror
  // dispatches `handleDOMEvents` for every event it receives, but it decides for
  // itself whether a click also becomes a `handleClick` call -- and after
  // `applyLinkHref` rewrites the document and refocuses the editor, the NEXT
  // click intermittently never reached `handleClick` at all. The popover then
  // stayed shut with nothing logged and nothing thrown, because the only code
  // that could reopen it never ran: saving a URL and clicking the link again
  // failed to reopen the editor roughly one time in three.
  //
  // `posAtCoords` is what both handlers resolve through now, so they also agree
  // on which run was pressed. `handleClick` was given a position ProseMirror had
  // already mapped through its own selection handling, which is a second way for
  // the two to disagree about the same gesture.
  return $prose(() => new Plugin({
    key: new PluginKey('link-click'),
    props: {
      handleDOMEvents: {
        pointerdown(view, event) {
          const at = view.posAtCoords({ left: event.clientX, top: event.clientY })
          const range = at ? linkRangeAt(view.state, at.pos) : null
          press.arm(pressClosesPopover(range, handlers.getLinkRange(), handlers.getLinkPopoverOpen()))
          return false
        },
        // The one signal that a press ended with no click behind it. Disarming
        // here keeps a scroll that started on a link from handing its intent to
        // whatever click arrives next.
        pointercancel() {
          press.disarm()
          return false
        },
        click(view, event) {
          const at = view.posAtCoords({ left: event.clientX, top: event.clientY })
          const range = at ? linkRangeAt(view.state, at.pos) : null
          // Read and disarm in one step, BEFORE the branch: the press is spent
          // whichever way the click goes, and doing it once means neither exit
          // can forget.
          const closesPopover = press.take(() =>
            pressClosesPopover(range, handlers.getLinkRange(), handlers.getLinkPopoverOpen()))
          if (clickAction(range, closesPopover, view.state.selection.empty) === 'close') {
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
    },
  }))
}
